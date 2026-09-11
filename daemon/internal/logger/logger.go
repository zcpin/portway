package logger

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// LogLevel represents the severity of a log message
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

var (
	globalLogger     *Logger
	globalLoggerOnce sync.Once
)

// LogHook receives normalized log events.
// level is one of: debug, info, warn, error.
type LogHook func(level string, message string)

// Logger is a thread-safe logger with level support
type Logger struct {
	mu       sync.RWMutex
	outLog   *log.Logger
	errLog   *log.Logger
	level    LogLevel
	prefix   string
	out      io.Writer
	errOut   io.Writer
	useColor bool
	hook     LogHook
	bufPool  sync.Pool
}

// ParseLevel is shared by initial configuration and runtime level updates.
func ParseLevel(level string) (LogLevel, error) {
	switch strings.TrimSpace(level) {
	case "debug":
		return DEBUG, nil
	case "info":
		return INFO, nil
	case "warn", "warning":
		return WARN, nil
	case "error":
		return ERROR, nil
	default:
		return INFO, fmt.Errorf("invalid log level: %s", level)
	}
}

// NewLogger creates a new logger with the specified level and output
func NewLogger(level string, out io.Writer, errOut io.Writer, useColor bool) (*Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	if out == nil {
		out = os.Stdout
	}
	if errOut == nil {
		errOut = os.Stderr
	}

	return &Logger{
		outLog:   log.New(out, "", 0),
		errLog:   log.New(errOut, "", 0),
		level:    lvl,
		out:      out,
		errOut:   errOut,
		useColor: useColor,
		bufPool: sync.Pool{
			New: func() interface{} {
				return &bytes.Buffer{}
			},
		},
	}, nil
}

// InitGlobalLogger initializes the global logger
func InitGlobalLogger(level string, useColor bool) error {
	return InitGlobalLoggerWithWriters(level, os.Stdout, os.Stderr, useColor)
}

// InitGlobalLoggerWithWriters initializes the global logger with explicit writers.
func InitGlobalLoggerWithWriters(level string, out io.Writer, errOut io.Writer, useColor bool) error {
	var err error
	globalLoggerOnce.Do(func() {
		globalLogger, err = NewLogger(level, out, errOut, useColor)
	})
	return err
}

// GetGlobalLogger returns the global logger
func GetGlobalLogger() *Logger {
	// 每个读取方都经过 Once，等待初始化完成后再访问指针及其字段。
	// 不能先无锁检查 globalLogger，否则会与首次初始化并发读写。
	_ = InitGlobalLogger("info", true)
	return globalLogger
}

// SetLevel sets the log level
func (l *Logger) SetLevel(level LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// SetPrefix sets the prefix for all log messages
func (l *Logger) SetPrefix(prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prefix = prefix
}

// SetHook sets the log hook for this logger.
func (l *Logger) SetHook(hook LogHook) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hook = hook
}

// Debug logs a debug message
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(DEBUG, format, args...)
}

// Info logs an info message
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(INFO, format, args...)
}

// Warn logs a warning message
func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(WARN, format, args...)
}

// Error logs an error message
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(ERROR, format, args...)
}

// log is the internal logging method
func (l *Logger) log(level LogLevel, format string, args ...interface{}) {
	l.mu.RLock()
	currentLevel := l.level
	prefix := l.prefix
	useColor := l.useColor
	hook := l.hook
	l.mu.RUnlock()

	if level < currentLevel {
		return
	}

	var levelStr string
	var levelName string
	var colorReset, colorCode string

	if useColor {
		colorReset = "\033[0m"
		switch level {
		case DEBUG:
			colorCode = "\033[36m" // Cyan
			levelStr = "DEBUG"
			levelName = "debug"
		case INFO:
			colorCode = "\033[32m" // Green
			levelStr = "INFO "
			levelName = "info"
		case WARN:
			colorCode = "\033[33m" // Yellow
			levelStr = "WARN "
			levelName = "warn"
		case ERROR:
			colorCode = "\033[31m" // Red
			levelStr = "ERROR"
			levelName = "error"
		}
	} else {
		switch level {
		case DEBUG:
			levelStr = "DEBUG"
			levelName = "debug"
		case INFO:
			levelStr = "INFO "
			levelName = "info"
		case WARN:
			levelStr = "WARN "
			levelName = "warn"
		case ERROR:
			levelStr = "ERROR"
			levelName = "error"
		}
	}

	if prefix != "" {
		prefix = "[" + prefix + "] "
	}

	plain := fmt.Sprintf(format, args...)

	buf := l.bufPool.Get().(*bytes.Buffer)
	buf.Reset()

	if useColor {
		fmt.Fprintf(buf, "%s[%s]%s%s ", colorCode, levelStr, colorReset, prefix)
	} else {
		fmt.Fprintf(buf, "[%s]%s", levelStr, prefix)
	}
	buf.WriteString(plain)
	message := buf.String()
	l.bufPool.Put(buf)

	output := l.outLog
	if level >= ERROR {
		output = l.errLog
	}
	output.Println(message)
	if hook != nil {
		hook(levelName, plain)
	}
}

// IsTerminal 判断 w 是否连接到终端，用于决定日志是否输出颜色。
// 服务模式下 stdout 被重定向到文件，不应写入 ANSI 转义序列。
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Global convenience functions

func Debug(format string, args ...interface{}) {
	GetGlobalLogger().Debug(format, args...)
}

func Info(format string, args ...interface{}) {
	GetGlobalLogger().Info(format, args...)
}

func Warn(format string, args ...interface{}) {
	GetGlobalLogger().Warn(format, args...)
}

func Error(format string, args ...interface{}) {
	GetGlobalLogger().Error(format, args...)
}

// SetGlobalLogHook sets the log hook for the global logger.
func SetGlobalLogHook(hook LogHook) {
	GetGlobalLogger().SetHook(hook)
}
