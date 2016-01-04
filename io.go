package main

import (
	"fmt"
	"github.com/cavaliercoder/go-rpm/yum"
	"github.com/pivotal-golang/bytefmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const (
	LOG_CAT_ERROR = iota
	LOG_CAT_WARN
	LOG_CAT_INFO
	LOG_CAT_DEBUG
)

var (
	logfileHandle *os.File    = nil
	logger        *log.Logger = nil
)

type DownloadJob struct {
	Label        string
	URL          string
	Size         uint64
	Path         string
	Checksum     string
	ChecksumType string
	Index        int
	Error        error
}

func InitLogFile() {
	if LogFilePath == "" {
		return
	}

	f, err := os.OpenFile(LogFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	PanicOn(err)

	logger = log.New(f, "", log.LstdFlags)
}

// CloseLogFile cleans up any file handles associates with the log file.
func CloseLogFile() {
	if logfileHandle != nil {
		PanicOn(logfileHandle.Close())
	}
}

// Logf prints output to a logfile with a category and timestamp
func Logf(category int, format string, a ...interface{}) {
	var cat string
	switch category {
	case LOG_CAT_ERROR:
		cat = "ERROR"
	case LOG_CAT_WARN:
		cat = "WARNING"
	case LOG_CAT_INFO:
		cat = "INFO"
	case LOG_CAT_DEBUG:
		cat = "DEBUG"
	default:
		panic(fmt.Sprintf("Unrecognized log category: %s", category))
	}

	logger.Printf("%s %s", cat, fmt.Sprintf(format, a...))
}

// Printf prints output to STDOUT or the logfile
func Printf(format string, a ...interface{}) {
	if logger == nil {
		fmt.Printf(format, a...)
	} else {
		Logf(LOG_CAT_INFO, format, a...)
	}
}

// Errorf prints an error message to log or STDOUT
func Errorf(err error, format string, a ...interface{}) {
	if logger == nil {
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %s: %s\n", fmt.Sprintf(format, a...), err.Error())
		} else {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", fmt.Sprintf(format, a...))
		}
	} else {
		if err != nil {
			Logf(LOG_CAT_ERROR, "%s: %s\n", fmt.Sprintf(format, a...), err.Error())
		} else {
			Logf(LOG_CAT_ERROR, format, a...)
		}
	}
}

// Fatalf prints an error message to log or STDOUT and exits the program with
// a non-zero exit code
func Fatalf(err error, format string, a ...interface{}) {
	Errorf(err, format, a...)
	os.Exit(1)
}

// Dprintf prints verbose output only if debug mode is enabled
func Dprintf(format string, a ...interface{}) {
	if DebugMode {
		if logger == nil {
			fmt.Fprintf(os.Stderr, fmt.Sprintf("DEBUG: %s", format), a...)
		} else {
			Logf(LOG_CAT_DEBUG, format, a...)
		}
	}
}

// URLJoin naively joins paths of a URL to enforce a single '/' separator
// between each segment.
func urljoin(v ...string) string {
	url := ""

	for _, s := range v {
		if url == "" {
			url = s
		} else if s != "" {
			url = fmt.Sprintf("%s/%s", strings.TrimRight(url, "/"), strings.TrimLeft(s, "/"))
		}
	}

	return url
}

// Download downloads multiple files asynchronously.
func Download(jobs []DownloadJob, complete chan<- DownloadJob) error {
	// always close complete channel
	defer func() {
		if complete != nil {
			close(complete)
		}
	}()

	// exit if no jobs
	if len(jobs) == 0 {
		return nil
	}

	// TODO: delete partially downloaded files on SIGINT

	// start producer
	c := make(chan DownloadJob, 0)
	go func() {
		for i, job := range jobs {
			job.Index = i + 1
			c <- job
		}
		close(c)
	}()

	// start consumers
	done := make(chan bool, 0)
	for i := 0; i < DownloadThreads; i++ {
		go func() {
			for job := range c {

				// http request
				Dprintf("[ %d / %d ] Downloading %s (%s)...\n", job.Index, len(jobs), job.Label, bytefmt.ByteSize(job.Size))
				if resp, err := http.Get(job.URL); err != nil {
					job.Error = err
					goto JobDone

				} else {
					defer resp.Body.Close()

					// check response code
					if resp.StatusCode != http.StatusOK {
						job.Error = fmt.Errorf("Bad status: %v", resp.Status)
						goto JobDone
					}

					// open local file for writing
					if w, err := os.Create(job.Path); err != nil {
						job.Error = err
						goto JobDone

					} else {
						defer w.Close()

						// download
						_, err = io.Copy(w, resp.Body)
						if err != nil {
							job.Error = err
							goto JobDone
						}
					}
				}

				// validate checksum
				if err := yum.ValidateFileChecksum(job.Path, job.Checksum, job.ChecksumType); err == yum.ErrChecksumMismatch {
					job.Error = err
					goto JobDone

				} else if err != nil {
					job.Error = fmt.Errorf("Checksum validation error: %v", err)
					goto JobDone
				}

			JobDone:

				// update caller or print any errors
				if complete != nil {
					complete <- job

				} else if job.Error != nil {
					Errorf(job.Error, "Error downloading %v", job.Label)
				}
			}

			done <- true
		}()
	}

	// wait for consumers to finish
	for i := 0; i < DownloadThreads; i++ {
		<-done
	}

	return nil
}
