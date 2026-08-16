package response

import (
	"errors"
	"net/http"
	"time"

	"github.com/AdityaIrfan/e-wallet-system-vocagame/wallet"
	"github.com/labstack/echo/v4"
)

// 200 OK Response
type OKResponse struct {
	Success bool   `json:"success" example:"true"`
	Status  int    `json:"status" example:"200"`
	Code    string `json:"code" example:"SUCCESS"`
	Message string `json:"message" example:"Operation completed successfully"`
	Data    any    `json:"data,omitempty"`
	Meta    Meta   `json:"meta"`
}

// 201 Created Response
type CreatedResponse struct {
	Success bool   `json:"success" example:"true"`
	Status  int    `json:"status" example:"201"`
	Code    string `json:"code" example:"CREATED"`
	Message string `json:"message" example:"Resource successfully created"`
	Data    any    `json:"data,omitempty"`
	Meta    Meta   `json:"meta"`
}

// 400 Bad Request
type BadRequestResponse struct {
	Success      bool         `json:"success" example:"false"`
	Status       int          `json:"status" example:"400"`
	Code         string       `json:"code" example:"BAD_REQUEST"`
	Message      string       `json:"message" example:"Invalid payload or parameters"`
	ErrorDetails []FieldError `json:"details,omitempty"`
	Meta         Meta         `json:"meta"`
}

// 404 Not Found
type NotFoundResponse struct {
	Success bool   `json:"success" example:"false"`
	Status  int    `json:"status" example:"404"`
	Code    string `json:"code" example:"NOT_FOUND"`
	Message string `json:"message" example:"Resource not found"`
	Meta    Meta   `json:"meta"`
}

// 409 Conflict
type ConflictResponse struct {
	Success bool   `json:"success" example:"false"`
	Status  int    `json:"status" example:"409"`
	Code    string `json:"code" example:"CONFLICT"`
	Message string `json:"message" example:"Resource already exists"`
	Meta    Meta   `json:"meta"`
}

// 500 Internal Server Error
type InternalServerErrorResponse struct {
	Success bool   `json:"success" example:"false"`
	Status  int    `json:"status" example:"500"`
	Code    string `json:"code" example:"INTERNAL_SERVER_ERROR"`
	Message string `json:"message" example:"Internal server error"`
	Meta    Meta   `json:"meta"`
}

type FieldError struct {
	Field string `json:"field" example:"name"`
	Issue string `json:"issue" example:"name is required."`
}

type Meta struct {
	Timestamp string `json:"timestamp" example:"2026-08-16T09:28:00Z"`
}

func generateMeta() Meta {
	return Meta{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// Success Helper (Return error wajib untuk Echo)
func Success(c echo.Context, httpStatus int, code string, message string, data any) error {
	switch httpStatus {
	case http.StatusCreated:
		return c.JSON(http.StatusCreated, OKResponse{
			Success: true,
			Status:  http.StatusCreated,
			Code:    "CREATED",
			Message: message,
			Data:    data,
			Meta:    generateMeta(),
		})
	default:
		return c.JSON(httpStatus, OKResponse{
			Success: true,
			Status:  http.StatusOK,
			Code:    "SUCCESS",
			Message: message,
			Data:    data,
			Meta:    generateMeta(),
		})
	}
}

func BadRequest(c echo.Context, errDesc string, errorDetails ...FieldError) error {
	c.JSON(http.StatusBadRequest, BadRequestResponse{
		Success:      false,
		Status:       http.StatusBadRequest,
		Code:         "BAD_REQUEST",
		Message:      errDesc,
		ErrorDetails: errorDetails,
		Meta:         generateMeta()})
	return errors.New(errDesc)
}

// Error Helper (Domain error mapping)
func Error(c echo.Context, err error, errorDetails ...FieldError) error {
	switch {
	case errors.Is(err, wallet.ErrWalletNotFound),
		errors.Is(err, wallet.ErrOwnerNotFound):
		return c.JSON(http.StatusNotFound, NotFoundResponse{
			Success: false,
			Status:  http.StatusNotFound,
			Code:    "NOT_FOUND",
			Message: err.Error(),
			Meta:    generateMeta()})

	case errors.Is(err, wallet.ErrWalletAlreadyExists),
		errors.Is(err, wallet.ErrOwnerAlreadyExists),
		errors.Is(err, wallet.ErrIdempotencyConflict):
		return c.JSON(http.StatusConflict, ConflictResponse{
			Success: false,
			Status:  http.StatusConflict,
			Code:    "CONFLICT",
			Message: err.Error(),
			Meta:    generateMeta()})

	case errors.Is(err, wallet.ErrOptimisticLock):
		return c.JSON(http.StatusConflict, ConflictResponse{
			Success: false,
			Status:  http.StatusConflict,
			Code:    "CONCURRENT_MODIFICATION",
			Message: err.Error(),
			Meta:    generateMeta()})

	case errors.Is(err, wallet.ErrInvalidAmount),
		errors.Is(err, wallet.ErrCurrencyMismatch),
		errors.Is(err, wallet.ErrSameWallet),
		errors.Is(err, wallet.ErrUnsupportedCurrency):
		return c.JSON(http.StatusBadRequest, BadRequestResponse{
			Success:      false,
			Status:       http.StatusBadRequest,
			Code:         "BAD_REQUEST",
			Message:      err.Error(),
			ErrorDetails: errorDetails,
			Meta:         generateMeta()})

	case errors.Is(err, wallet.ErrInsufficientBalance),
		errors.Is(err, wallet.ErrWalletSuspended):
		return c.JSON(http.StatusUnprocessableEntity, ConflictResponse{
			Success: false,
			Status:  http.StatusConflict,
			Code:    "UNPROCESSABLE_ENTITY",
			Message: err.Error(),
			Meta:    generateMeta()})

	default:
		c.Logger().Errorf("unhandled error: %v", err)
		return c.JSON(http.StatusInternalServerError, ConflictResponse{
			Success: false,
			Status:  http.StatusInternalServerError,
			Code:    "INTERNAL_SERVER_ERROR",
			Message: "internal server error",
			Meta:    generateMeta()})
	}
}
