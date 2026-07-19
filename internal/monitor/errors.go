package monitor

import (
	"errors"
	"fmt"
)

// ErrorClass 表示调度器可稳定判断的错误类别。
type ErrorClass string

const (
	ErrorClassConfig   ErrorClass = "config"
	ErrorClassAuth     ErrorClass = "auth"
	ErrorClassNetwork  ErrorClass = "network"
	ErrorClassServer   ErrorClass = "server"
	ErrorClassResponse ErrorClass = "response"
	ErrorClassRemote   ErrorClass = "remote"
)

// ErrorKind 是主线告警逻辑使用的错误分类名称。
type ErrorKind = ErrorClass

const (
	ErrorKindConfig   ErrorKind = ErrorClassConfig
	ErrorKindAuth     ErrorKind = ErrorClassAuth
	ErrorKindNetwork  ErrorKind = ErrorClassNetwork
	ErrorKindServer   ErrorKind = ErrorClassServer
	ErrorKindResponse ErrorKind = ErrorClassResponse
	ErrorKindRemote   ErrorKind = ErrorClassRemote
)

// CheckError 对外只暴露经过清理的说明，避免响应正文或凭据进入日志。
type CheckError struct {
	Kind       ErrorKind
	Operation  string
	StatusCode int
	Message    string
	cause      error
}

func (e *CheckError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s失败", e.Operation)
	}
	return "渠道检测失败"
}

func (e *CheckError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func checkError(class ErrorClass, operation, message string, statusCode int, cause error) error {
	return &CheckError{
		Kind:       class,
		Operation:  operation,
		StatusCode: statusCode,
		Message:    message,
		cause:      cause,
	}
}

// ErrorClassOf 返回错误类别，未知错误归为响应错误。
func ErrorClassOf(err error) ErrorClass {
	var target *CheckError
	if errors.As(err, &target) {
		return target.Kind
	}
	return ErrorClassResponse
}

// IsAuthFailure 判断错误是否由凭据失效或权限不足引起。
func IsAuthFailure(err error) bool {
	return ErrorClassOf(err) == ErrorClassAuth
}
