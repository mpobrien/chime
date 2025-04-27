package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

const chimeDBPathEnvKey = "CHIME_DB_PATH"

const (
	helpCommandName   = "help"
	runCommandName    = "run"
	takeCommandName   = "take"
	listCommandName   = "list"
	addCommandName    = "add"
	removeCommandName = "remove"
)

type globalArgs struct {
	dbPath string
}

type run struct {
	globalArgs
	numWorkers int
}

type take struct {
	globalArgs
	jobID int
}
type list struct {
	globalArgs
}
type add struct {
	globalArgs
	commandToRun string
}
type remove struct {
	globalArgs
	id int
}

func main() {
	var dbPath string
	flag.StringVar(&dbPath, "dbpath", "", "path to DB file")
	flag.Parse()

	if len(dbPath) == 0 {
		var ok bool
		if dbPath, ok = os.LookupEnv(chimeDBPathEnvKey); !ok {
			homedir, err := os.UserHomeDir()
			if err != nil {
				log.Fatalf("failed to find home dir: %s", err)
			}

			dbPath = filepath.Join(homedir, ".chime.db")
		}
	}

	cmd, err := parseSubcommand(
		globalArgs{
			dbPath: dbPath,
		},
		flag.Args(),
	)
	if err != nil {
		log.Printf("%s", err)
		os.Exit(1)
	}

	if err := cmd.Run(); err != nil {
		log.Printf("%s", err)
		os.Exit(1)
	}
}

type subcommand interface {
	Run() error
}

func (r run) Run() error {
	db, err := Open(r.globalArgs.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	defer db.Close()

	jobs := make(chan *Job)
	var numJobs int

	// Channel to collect errors from async tasks;
	// 1 per consumer plus one for producer.
	errs := make(chan error, r.numWorkers+1)

	// Start a worker to pull jobs from DB and push into queue.
	go func() {
		var err error
		numJobs, err = runProducerWorker(db, jobs)
		errs <- err
	}()

	for i := 0; i < r.numWorkers; i++ {
		go func() {
			errs <- runConsumerWorker(i, db, jobs)
		}()
	}

	numErrs := 0
	for i := 0; i < r.numWorkers+1; i++ {
		if err := <-errs; err != nil {
			numErrs++
			log.Printf("Error: %v", err)
		}
	}

	log.Printf("finished after processing %d jobs (%d errors)", numJobs, numErrs)
	return nil
}

func runProducerWorker(db *DB, jobs chan<- *Job) (int, error) {
	defer close(jobs)
	numJobs := 0
	for {
		nextJob, err := db.TakeNextJob()
		if err != nil {
			return numJobs, fmt.Errorf("failed to read next job from DB: %w", err)
		}
		if nextJob == nil {
			return numJobs, nil
		}
		numJobs++
		jobs <- nextJob
	}
	return numJobs, nil
}

func runConsumerWorker(workerId int, db *DB, jobs <-chan *Job) error {
	for job := range jobs {
		if err := execJob(db, job); err != nil {
			return err
		}
	}
	return nil
}

func (t take) Run() error {
	db, err := Open(t.globalArgs.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	defer db.Close()

	nextJob, err := db.TakeNextJob()
	if err != nil {
		return err
	}
	if nextJob == nil {
		return nil
	}

	return execJob(db, nextJob)
}

func (cmd list) Run() error {
	log.Printf("opening db at path: %s", cmd.globalArgs.dbPath)
	db, err := Open(cmd.globalArgs.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	defer db.Close()

	jobs, err := db.ListJobs()
	if err != nil {
		return fmt.Errorf("failed to list jobs: %w", err)
	}

	headerStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		PaddingRight(2).Foreground(lipgloss.Color("#ffffff")).Bold(true)
	cellStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		PaddingRight(2).Foreground(lipgloss.Color("#ffffff"))
	pendingStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		PaddingRight(2).Foreground(lipgloss.Color("#888888"))
	progressStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		PaddingRight(2).Foreground(lipgloss.Color("#ffff00"))
	successStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		PaddingRight(2).Foreground(lipgloss.Color("#00ff00"))
	failedStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		PaddingRight(2).Foreground(lipgloss.Color("196"))

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("99"))).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row < 0 {
				return headerStyle
			}
			if col != 1 {
				return cellStyle
			}
			switch jobs[row].Status {
			case statusPending:
				return pendingStyle
			case statusInProgress:
				return progressStyle
			case statusDoneSuccess:
				return successStyle
			case statusDoneFailed:
				return failedStyle
			}
			return cellStyle
		}).
		// StyleFunc(func(row, col int) lipgloss.Style {
		// 	switch {
		// 	case row == 0:
		// 		return lipgloss.NewStyle()
		// 	case row%2 == 0:
		// 		return lipgloss.NewStyle()
		// 	default:
		// 		return OddRowStyle
		// 	}
		// }).
		Headers("ID", "STATUS", "COMMAND")

	for _, job := range jobs {
		t.Row(JobToRow(job)...)
	}

	fmt.Print(t)

	return err
}

func JobRowStyles() {

}

func JobToRow(job Job) []string {
	out := []string{
		fmt.Sprintf("%d", job.ID),
	}
	switch job.Status {
	case statusPending:
		out = append(out, "Pending")
	case statusInProgress:
		out = append(
			out,
			fmt.Sprintf("Running (%s)", time.Now().Sub(job.StartedAtTime())),
		)
	case statusDoneSuccess:
		out = append(
			out,
			fmt.Sprintf("Done (%s)", job.FinishedAtTime().Sub(job.StartedAtTime())),
		)
	case statusDoneFailed:
		out = append(out, "Failed")
	}
	out = append(out, job.Command)
	return out
}

func (cmd remove) Run() error {
	db, err := Open(cmd.globalArgs.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	defer db.Close()

	_, err = db.DeleteJob(int64(cmd.id))
	if err != nil {
		return err
	}
	return nil
}

func (cmd add) Run() error {
	db, err := Open(cmd.globalArgs.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	defer db.Close()

	jobID, err := db.AddJob(cmd.commandToRun)
	if err != nil {
		return err
	}

	log.Printf("added job #%d", jobID)
	return nil
}

func parseSubcommand(globals globalArgs, args []string) (subcommand, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no args specified")
	}
	cmd, args := args[0], args[1:]

	switch cmd {
	case runCommandName:
		numWorkers := 1
		var err error
		if len(args) > 0 {
			if numWorkers, err = strconv.Atoi(args[0]); err != nil {
				return nil, fmt.Errorf("invalid value for number of workers")
			}
		}
		if numWorkers < 1 {
			numWorkers = 1
		}

		return run{
			globalArgs: globals,
			numWorkers: numWorkers,
		}, nil
	case takeCommandName:
		var jobID int
		var err error
		if len(args) == 1 {
			if jobID, err = strconv.Atoi(args[0]); err != nil {
				return nil, fmt.Errorf("param required: command to run")
			}
		}
		return take{globalArgs: globals, jobID: jobID}, nil
	case listCommandName:
		return list{globalArgs: globals}, nil
	case addCommandName:
		if len(args) != 1 {
			return nil, fmt.Errorf("param required: command to run")
		}
		return add{globalArgs: globals, commandToRun: args[0]}, nil
	case removeCommandName:
		if len(args) != 1 {
			return nil, fmt.Errorf("param required: job ID to remove")
		}
		jobID, err := strconv.Atoi(args[0])
		if err != nil {
			return nil, fmt.Errorf("invalid job ID: '%s'", args[0])
		}
		return remove{
			globalArgs: globals,
			id:         jobID,
		}, nil
	}
	return nil, fmt.Errorf("unknown command: '%s'", cmd)
}

func execJob(db *DB, nextJob *Job) error {
	cmd := exec.Command("sh", "-c", nextJob.Command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	runJobErr := func() error {
		if err := cmd.Start(); err != nil {
			return err
		}
		if err := db.SetJobPID(int64(nextJob.ID), int64(cmd.Process.Pid)); err != nil {
			log.Printf("failed to set job pid: %s", err)
		}

		if err := cmd.Wait(); err != nil {
			return err
		}
		return nil
	}()

	if runJobErr != nil {
		if err := db.SetJobStatus(int64(nextJob.ID), int64(statusDoneFailed)); err != nil {
			return fmt.Errorf("failed to set job status to failed (%s) for job error: %s", err, runJobErr)
		}
	} else {
		if err := db.SetJobStatus(int64(nextJob.ID), int64(statusDoneSuccess)); err != nil {
			return fmt.Errorf("failed to set job status to success for job error: %s", runJobErr)
		}
	}

	return nil
}
