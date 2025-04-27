package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	lock *sync.Mutex
	*sql.DB
}

const (
	statusPending     int = 0
	statusInProgress  int = 1
	statusDoneSuccess int = 2
	statusDoneFailed  int = 3
)

type Job struct {
	ID         int    `db:"id"`
	Command    string `db:"command"`
	PID        int    `db:"pid"`
	Status     int    `db:"status"`
	CreatedAt  int64  `db:"created_at"`
	StartedAt  int64  `db:"started_at"`
	FinishedAt int64  `db:"finished_at"`
}

func (job Job) CreatedAtTime() time.Time {
	return time.UnixMilli(job.CreatedAt)
}

func (job Job) FinishedAtTime() time.Time {
	return time.UnixMilli(job.FinishedAt)
}

func (job Job) StartedAtTime() time.Time {
	return time.UnixMilli(job.StartedAt)
}

func (job Job) String() string {
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("%d: ", job.ID))
	switch job.Status {
	case statusPending:
		sb.WriteString("[ ] ")
	case statusInProgress:
		sb.WriteString("[-] ")
	case statusDoneSuccess:
		sb.WriteString("[x] ")
	case statusDoneFailed:
		sb.WriteString("[!] ")
	}
	sb.WriteString(job.Command)
	if job.PID > 0 {
		sb.WriteString(fmt.Sprintf(" [%d]", job.PID))
	}
	if job.StartedAt != 0 {
		if job.FinishedAt != 0 {
			elapsed := job.FinishedAtTime().Sub(job.StartedAtTime())
			sb.WriteString(fmt.Sprintf(" %s", elapsed))
		} else {
			elapsed := time.Since(job.StartedAtTime())
			sb.WriteString(fmt.Sprintf(" %s", elapsed))
		}
	}

	return sb.String()
}

func (db *DB) SetJobPID(jobID int64, pid int64) error {
	db.lock.Lock()
	defer db.lock.Unlock()
	_, err := db.Exec("UPDATE jobs SET pid=? WHERE id=?", pid, jobID)
	return err
}

func (db *DB) SetJobStatus(jobID int64, status int64) error {
	db.lock.Lock()
	defer db.lock.Unlock()
	_, err := db.Exec("UPDATE jobs SET status=?, finished_at=? WHERE id=?", status, time.Now().UnixMilli(), jobID)
	return err
}

func (db *DB) TakeNextJob() (*Job, error) {
	db.lock.Lock()
	defer db.lock.Unlock()
	var job Job
	if err := db.QueryRow(`
	WITH selected_job AS (
		SELECT * FROM jobs
		WHERE status = 0
		ORDER BY id ASC
		LIMIT 1
	)
	UPDATE jobs SET status = 1, started_at=?
	WHERE id = (SELECT id FROM selected_job)
	RETURNING id, command, pid, status, created_at, started_at, finished_at;
	`,
		time.Now().UnixMilli(),
	).
		Scan(
			&job.ID,
			&job.Command,
			&job.PID,
			&job.Status,
			&job.CreatedAt,
			&job.StartedAt,
			&job.FinishedAt,
		); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

// Deletes job with given ID. Returns true if the job existed.
func (db *DB) DeleteJob(id int64) (bool, error) {
	db.lock.Lock()
	defer db.lock.Unlock()
	result, err := db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (db *DB) ListJobs() ([]Job, error) {
	db.lock.Lock()
	defer db.lock.Unlock()
	rows, err := db.Query(`SELECT id, command, pid, status, created_at, started_at, finished_at FROM JOBS`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job

	for rows.Next() {
		var job Job
		if err := rows.Scan(
			&job.ID,
			&job.Command,
			&job.PID,
			&job.Status,
			&job.CreatedAt,
			&job.StartedAt,
			&job.FinishedAt,
		); err != nil {
			return jobs, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (db *DB) AddJob(command string) (int64, error) {
	db.lock.Lock()
	defer db.lock.Unlock()

	result, err := db.Exec(`
	BEGIN TRANSACTION;
	INSERT INTO jobs (command, status, created_at, started_at, finished_at)  values (?,?,?,?,?);
	COMMIT TRANSACTION;
	`, command, statusPending, time.Now().UnixMilli(), 0, 0)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func Open(filename string) (*DB, error) {
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		return nil, err
	}

	sqlStmt := `
	BEGIN TRANSACTION;
	create table if not exists jobs 
	(
		id integer not null primary key,
		command text not null,
		pid integer default 0,
		status integer default 0,
		created_at int default 0,
		started_at int default 0,
		finished_at int default 0
	);
	COMMIT TRANSACTION;
	`
	_, err = db.Exec(sqlStmt)
	if err != nil {
		return nil, err
	}

	return &DB{
		lock: &sync.Mutex{},
		DB:   db,
	}, nil
}
