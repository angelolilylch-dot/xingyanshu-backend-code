// Package logx provides plain-text, daily-rotated operational logs.
package logx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm/logger"
)

type contextKey string

const requestIDContextKey contextKey = "request_id"

// Settings is deliberately small: log files are configured by the application
// configuration, never by request data.
type Settings struct {
	Directory     string
	RetentionDays int
	AlsoStdout    bool
	Component     string
}

var (
	appLogger      = log.Default()
	errorLogger    = log.Default()
	accessLogger   = log.Default()
	securityLogger = log.Default()
)

// Init configures daily text log files for one process (server or worker).
func Init(settings Settings) (func(), error) {
	if settings.Directory == "" {
		return nil, fmt.Errorf("log directory is required")
	}
	if settings.Component == "" {
		settings.Component = "server"
	}
	if settings.RetentionDays <= 0 {
		return nil, fmt.Errorf("log retention days must be positive")
	}
	if err := os.MkdirAll(settings.Directory, 0o750); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}

	newWriter := func(name string) (*dailyWriter, error) {
		return newDailyWriter(settings.Directory, settings.Component+"-"+name, settings.RetentionDays)
	}
	appFile, err := newWriter("app")
	if err != nil {
		return nil, err
	}
	errorFile, err := newWriter("error")
	if err != nil {
		_ = appFile.Close()
		return nil, err
	}
	accessFile, err := newWriter("access")
	if err != nil {
		_ = appFile.Close()
		_ = errorFile.Close()
		return nil, err
	}
	securityFile, err := newWriter("security")
	if err != nil {
		_ = appFile.Close()
		_ = errorFile.Close()
		_ = accessFile.Close()
		return nil, err
	}

	withConsole := func(file io.Writer) io.Writer {
		if settings.AlsoStdout {
			return io.MultiWriter(file, os.Stdout)
		}
		return file
	}
	flags := log.Ldate | log.Ltime | log.Lmicroseconds
	appLogger = log.New(withConsole(appFile), "INFO ", flags)
	errorLogger = log.New(withConsole(errorFile), "ERROR ", flags)
	accessLogger = log.New(withConsole(accessFile), "ACCESS ", flags)
	securityLogger = log.New(withConsole(securityFile), "SECURITY ", flags)
	// Existing log.Printf calls are retained, but are now persisted too.
	log.SetOutput(appLogger.Writer())
	log.SetFlags(flags)
	log.SetPrefix("INFO ")

	return func() {
		_ = appFile.Close()
		_ = errorFile.Close()
		_ = accessFile.Close()
		_ = securityFile.Close()
	}, nil
}

func Appf(format string, args ...any)    { appLogger.Print(fmt.Sprintf(format, args...)) }
func Errorf(format string, args ...any)  { errorLogger.Print(Redact(fmt.Sprintf(format, args...))) }
func Accessf(format string, args ...any) { accessLogger.Print(Redact(fmt.Sprintf(format, args...))) }
func Securityf(format string, args ...any) {
	securityLogger.Print(Redact(fmt.Sprintf(format, args...)))
}

// WithRequestID keeps the request correlation id available to services and GORM.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, requestID)
}
func RequestID(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDContextKey).(string); ok {
		return value
	}
	return ""
}

// GormLogger writes every database error, including the rendered SQL and its
// parameters, to the error log. Secrets are redacted before persistence.
type GormLogger struct{}

func (GormLogger) LogMode(logger.LogLevel) logger.Interface { return GormLogger{} }
func (GormLogger) Info(context.Context, string, ...any)     {}
func (GormLogger) Warn(context.Context, string, ...any)     {}
func (GormLogger) Error(context.Context, string, ...any)    {}
func (GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err == nil {
		return
	}
	sql, rows := fc()
	fields := fmt.Sprintf("request_id=%q stage=db.query elapsed_ms=%d rows=%d error_type=%T error=%q sql=%q", RequestID(ctx), time.Since(begin).Milliseconds(), rows, err, Redact(err.Error()), RedactSQL(sql))
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) {
		fields += fmt.Sprintf(" mysql_code=%d sql_state=%q", mysqlErr.Number, string(mysqlErr.SQLState[:]))
	}
	Errorf("%s", fields)
}

type dailyWriter struct {
	directory, prefix string
	retentionDays     int
	mu                sync.Mutex
	date              string
	file              *os.File
}

func newDailyWriter(directory, prefix string, retentionDays int) (*dailyWriter, error) {
	w := &dailyWriter{directory: directory, prefix: prefix, retentionDays: retentionDays}
	if err := w.rotateLocked(time.Now()); err != nil {
		return nil, err
	}
	return w, nil
}
func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotateLocked(time.Now()); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}
func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
func (w *dailyWriter) rotateLocked(now time.Time) error {
	date := now.Format("2006-01-02")
	if w.file != nil && w.date == date {
		return nil
	}
	if w.file != nil {
		_ = w.file.Close()
	}
	file, err := os.OpenFile(filepath.Join(w.directory, w.prefix+"-"+date+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open %s log: %w", w.prefix, err)
	}
	w.file, w.date = file, date
	entries, _ := os.ReadDir(w.directory)
	cutoff := now.AddDate(0, 0, -w.retentionDays)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), w.prefix+"-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(w.directory, entry.Name()))
		}
	}
	return nil
}

var (
	jsonSecret   = regexp.MustCompile(`(?i)("(?:password|passwd|token|authorization|cookie|verification_?code|captcha_?code|sms_?code|secret|credential)"\s*:\s*")[^"]*(")`)
	querySecret  = regexp.MustCompile(`(?i)([?&;\s](?:password|passwd|token|authorization|cookie|verification_?code|captcha_?code|sms_?code|secret|credential)=)[^&;\s]+`)
	headerSecret = regexp.MustCompile(`(?im)((?:authorization|cookie|set-cookie)\s*[:=]\s*)[^\r\n]+`)
	dsnSecret    = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:\s/]+:)[^@\s/]+(@)`)
	sqlSecret    = regexp.MustCompile(`(?i)\b(password|passwd|token|authorization|cookie|verification_?code|captcha_?code|sms_?code|secret|credential)\b`)
)

// Redact removes credentials and verification codes. Test logging intentionally
// keeps business PII such as mobile numbers and identity details for diagnosis.
func Redact(value string) string {
	value = jsonSecret.ReplaceAllString(value, `${1}***${2}`)
	value = querySecret.ReplaceAllString(value, `${1}***`)
	value = headerSecret.ReplaceAllString(value, `${1}***`)
	return dsnSecret.ReplaceAllString(value, `${1}***${2}`)
}

// RedactSQL keeps raw SQL parameters for diagnostics unless the statement
// references a credential or verification-code field. SQL interpolation does
// not preserve enough field/value structure to safely mask only that value.
func RedactSQL(sql string) string {
	if sqlSecret.MatchString(sql) {
		return "[REDACTED_SQL_CONTAINS_SECRET_FIELD]"
	}
	return Redact(sql)
}
