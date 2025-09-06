package logger

import (
	"io"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LogLevel 定义日志级别
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

// Logger 自定义日志器
type Logger struct {
	debugLogger *log.Logger
	infoLogger  *log.Logger
	warnLogger  *log.Logger
	errorLogger *log.Logger
	level       LogLevel
}

var (
	defaultLogger *Logger
)

// InitLogger 初始化日志系统
func InitLogger(level LogLevel, enableFileLogging bool, logFilePath string, maxLogSizeMB int, maxBackups int, maxAgeDays int) {
	defaultLogger = &Logger{
		level: level,
	}

	// 设置输出目标
	var output io.Writer = os.Stdout
	
	if enableFileLogging && logFilePath != "" {
		// 确保日志目录存在
		logDir := filepath.Dir(logFilePath)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			log.Printf("无法创建日志目录 %s: %v", logDir, err)
		} else {
			// 使用 lumberjack 实现日志轮转
			fileOutput := &lumberjack.Logger{
				Filename:   logFilePath,
				MaxSize:    maxLogSizeMB, // MB
				MaxBackups: maxBackups,   // 保留的旧日志文件数量
				MaxAge:     maxAgeDays,   // 保留的天数
				Compress:   true,         // 压缩旧日志文件
			}
			
			// 同时输出到控制台和文件
			output = io.MultiWriter(os.Stdout, fileOutput)
		}
	}

	// 创建不同级别的日志器
	defaultLogger.debugLogger = log.New(output, "DEBUG: ", log.LstdFlags|log.Lshortfile)
	defaultLogger.infoLogger = log.New(output, "INFO:  ", log.LstdFlags)
	defaultLogger.warnLogger = log.New(output, "WARN:  ", log.LstdFlags)
	defaultLogger.errorLogger = log.New(output, "ERROR: ", log.LstdFlags|log.Lshortfile)
}

// GetLogger 获取默认日志器
func GetLogger() *Logger {
	if defaultLogger == nil {
		// 如果未初始化，使用默认配置
		InitLogger(INFO, false, "", 100, 5, 30)
	}
	return defaultLogger
}

// Debug 输出调试日志
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level <= DEBUG {
		l.debugLogger.Printf(format, v...)
	}
}

// Debugln 输出调试日志（换行）
func (l *Logger) Debugln(v ...interface{}) {
	if l.level <= DEBUG {
		l.debugLogger.Println(v...)
	}
}

// Info 输出信息日志
func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= INFO {
		l.infoLogger.Printf(format, v...)
	}
}

// Infoln 输出信息日志（换行）
func (l *Logger) Infoln(v ...interface{}) {
	if l.level <= INFO {
		l.infoLogger.Println(v...)
	}
}

// Warn 输出警告日志
func (l *Logger) Warn(format string, v ...interface{}) {
	if l.level <= WARN {
		l.warnLogger.Printf(format, v...)
	}
}

// Warnln 输出警告日志（换行）
func (l *Logger) Warnln(v ...interface{}) {
	if l.level <= WARN {
		l.warnLogger.Println(v...)
	}
}

// Error 输出错误日志
func (l *Logger) Error(format string, v ...interface{}) {
	if l.level <= ERROR {
		l.errorLogger.Printf(format, v...)
	}
}

// Errorln 输出错误日志（换行）
func (l *Logger) Errorln(v ...interface{}) {
	if l.level <= ERROR {
		l.errorLogger.Println(v...)
	}
}

// Fatal 输出致命错误日志并退出程序
func (l *Logger) Fatal(format string, v ...interface{}) {
	l.errorLogger.Fatalf(format, v...)
}

// Fatalln 输出致命错误日志并退出程序
func (l *Logger) Fatalln(v ...interface{}) {
	l.errorLogger.Fatalln(v...)
}

// SetLevel 动态设置日志级别
func (l *Logger) SetLevel(level LogLevel) {
	l.level = level
}

// ParseLogLevel 解析日志级别字符串
func ParseLogLevel(levelStr string) LogLevel {
	switch levelStr {
	case "DEBUG", "debug":
		return DEBUG
	case "INFO", "info":
		return INFO
	case "WARN", "warn":
		return WARN
	case "ERROR", "error":
		return ERROR
	default:
		return INFO
	}
}

// 全局便捷函数
func Debug(format string, v ...interface{}) {
	GetLogger().Debug(format, v...)
}

func Debugln(v ...interface{}) {
	GetLogger().Debugln(v...)
}

func Info(format string, v ...interface{}) {
	GetLogger().Info(format, v...)
}

func Infoln(v ...interface{}) {
	GetLogger().Infoln(v...)
}

func Warn(format string, v ...interface{}) {
	GetLogger().Warn(format, v...)
}

func Warnln(v ...interface{}) {
	GetLogger().Warnln(v...)
}

func Error(format string, v ...interface{}) {
	GetLogger().Error(format, v...)
}

func Errorln(v ...interface{}) {
	GetLogger().Errorln(v...)
}

func Fatal(format string, v ...interface{}) {
	GetLogger().Fatal(format, v...)
}

func Fatalln(v ...interface{}) {
	GetLogger().Fatalln(v...)
}