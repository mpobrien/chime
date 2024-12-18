package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const chimeDBPathEnvKey = "CHIME_DB_PATH"

const (
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

	num := 0
	for {
		nextJob, err := db.TakeNextJob()
		if err != nil {
			return err
		}
		if nextJob == nil {
			break
		}
		if err := execJob(db, nextJob); err != nil {
			return err
		}
		num++
	}
	log.Printf("finished after processing %d jobs", num)
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
	db, err := Open(cmd.globalArgs.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	defer db.Close()

	jobs, err := db.ListJobs()
	if err != nil {
		return fmt.Errorf("failed to list jobs: %w", err)
	}
	for _, job := range jobs {
		// TODO tabwriter formatted output
		fmt.Println(job.String())
	}
	return err
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
		return run{globalArgs: globals}, nil
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
