package logging

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

// Logger is the global logger instance.
var Logger *log.Logger

// Init initializes the logger with a file output.
func Init() {
	// TODO: Add option to take log file path from user

	// os.TempDir gives temporary directory of any platform
	faustTempDir := filepath.Join(os.TempDir(), "faustlsp")
	os.Mkdir(faustTempDir, 0750)

	currTime := time.Now().Format("15-04-05")
	logFile := "log-" + currTime + ".txt"
	logFilePath := filepath.Join(faustTempDir, logFile)

	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_RDWR, 0755)
	if err != nil {
		panic(err)
	}

	// Initialize the logger to write to the file, without flags or prefixes.
	Logger = log.New(f, "faust-lsp: ", log.Ltime)
}
