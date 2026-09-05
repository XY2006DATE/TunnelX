package util

import (
	"fmt"
	"log"
	"os"
	"sync"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
	FATAL
)

var (
	logger     *Logger
	loggerOnce sync.Once
)

type Logger struct {
	level      LogLevel
	consoleLog *log.Logger
	fileLog    *log.Logger
	file       *os.File
	mu         sync.Mutex
}

func InitLogger(levelStr string, logFile string) error {
	var level LogLevel
	switch levelStr {
	case "debug":
		level = DEBUG
	case "info":
		level = INFO
	case "warn":
		level = WARN
	case "error":
		level = ERROR
	default:
		level = INFO
	}

	logger = &Logger{
		level:      level,
		consoleLog: log.New(os.Stdout, "", log.LstdFlags),
	}

	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		logger.file = file
		logger.fileLog = log.New(file, "", log.LstdFlags)
	}

	return nil
}

func GetLogger() *Logger {
	if logger == nil {
		logger = &Logger{
			level:      INFO,
			consoleLog: log.New(os.Stdout, "", log.LstdFlags),
		}
	}
	return logger
}

func (l *Logger) log(level LogLevel, format string, v ...interface{}) {
	if level < l.level {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	var prefix string
	switch level {
	case DEBUG:
		prefix = "[DEBUG] "
	case INFO:
		prefix = "[INFO]  "
	case WARN:
		prefix = "[WARN]  "
	case ERROR:
		prefix = "[ERROR] "
	case FATAL:
		prefix = "[FATAL] "
	}

	msg := fmt.Sprintf(prefix+format, v...)

	if l.consoleLog != nil {
		l.consoleLog.Println(msg)
	}
	if l.fileLog != nil {
		l.fileLog.Println(msg)
	}

	if level == FATAL {
		os.Exit(1)
	}
}

func Debug(format string, v ...interface{}) {
	GetLogger().log(DEBUG, format, v...)
}

func Info(format string, v ...interface{}) {
	GetLogger().log(INFO, format, v...)
}

func Warn(format string, v ...interface{}) {
	GetLogger().log(WARN, format, v...)
}

func Error(format string, v ...interface{}) {
	GetLogger().log(ERROR, format, v...)
}

func Fatal(format string, v ...interface{}) {
	GetLogger().log(FATAL, format, v...)
}

func Sync() {
	if logger != nil && logger.file != nil {
		logger.file.Sync()
	}
}
