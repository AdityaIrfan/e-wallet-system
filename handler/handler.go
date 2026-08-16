package handler

import (
	"fmt"
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"

	"github.com/AdityaIrfan/e-wallet-system-vocagame/response"
	"github.com/AdityaIrfan/e-wallet-system-vocagame/wallet"
)

type Handler struct {
	svc *wallet.Service
}

func New(svc *wallet.Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(e *echo.Echo) {
	e.POST("/owners", h.CreateOwner)
	e.GET("/owners", h.ListOwners)

	e.POST("/wallets", h.CreateWallet)
	e.GET("/wallets", h.ListWallets)
	e.GET("/wallets/:id", h.GetWallet)
	e.POST("/wallets/:id/topup", h.Topup)
	e.POST("/wallets/:id/pay", h.Pay)
	e.POST("/wallets/:id/suspend", h.SuspendWallet)
	e.POST("/wallets/transfer", h.Transfer)
	e.GET("/wallets/:id/ledger", h.LedgerHistory)
}

// ── Request Structs ───────────────────────────────────────────────────

type createOwnerRequest struct {
	Name string `json:"name" validate:"required,min=3,max=50" example:"John Doe"`
}

type createWalletRequest struct {
	OwnerID  string          `json:"owner_id"  validate:"required,uuid" example:"a1b2c3d4-e5f6-7890-abcd-1234567890ab" format:"uuid"`
	Currency wallet.Currency `json:"currency"  validate:"required,len=3" enums:"usd,eur,idr" swaggertype:"string" example:"usd"`
}

type amountRequest struct {
	Amount string `json:"amount"          validate:"required,numeric" example:"100.25"`
}

type transferRequest struct {
	FromWalletID string `json:"from_wallet_id"  validate:"required,uuid"`
	ToWalletID   string `json:"to_wallet_id"    validate:"required,uuid"`
	Amount       string `json:"amount"          validate:"required,numeric"`
}

type listWalletsQuery struct {
	OwnerID string `query:"owner_id" validate:"required,uuid"`
}

// ── Owner Handlers ────────────────────────────────────────────────────

// CreateOwner godoc
// @Summary      Register a new owner
// @Tags         owners
// @Accept       json
// @Produce      json
// @Param        body  body      createOwnerRequest  true  "Owner payload"
// @Success      201   {object}  response.CreatedResponse{data=wallet.Owner}
// @Failure      400   {object}  response.BadRequestResponse{details=[]response.FieldError}
// @Failure      409   {object}  response.ConflictResponse
// @Router       /owners [post]
func (h *Handler) CreateOwner(c echo.Context) error {
	var req createOwnerRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}
	o, err := h.svc.CreateOwner(req.Name)
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusCreated, "OWNER_CREATED", "Owner successfully created", o)
}

// ListOwners godoc
// @Summary      List all registered owners
// @Tags         owners
// @Produce      json
// @Success      200  {object}  response.OKResponse{data=[]wallet.Owner}
// @Failure      500  {object}  response.InternalServerErrorResponse
// @Router       /owners [get]
func (h *Handler) ListOwners(c echo.Context) error {
	owners, err := h.svc.ListOwners()
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "SUCCESS", "Owners retrieved successfully", owners)
}

// ── Wallet Handlers ───────────────────────────────────────────────────

// CreateWallet godoc
// @Summary      Create a wallet for an owner
// @Tags         wallets
// @Accept       json
// @Produce      json
// @Param        body  		body      createWalletRequest  true  "Wallet payload"
// @Success      201   		{object}  response.CreatedResponse{data=wallet.Wallet}
// @Failure      400   		{object}  response.BadRequestResponse{details=[]response.FieldError}
// @Failure      404   		{object}  response.NotFoundResponse
// @Failure      409   		{object}  response.ConflictResponse
// @Failure      500   		{object}  response.InternalServerErrorResponse
// @Router       /wallets [post]
func (h *Handler) CreateWallet(c echo.Context) error {
	var req createWalletRequest
	if err := bindAndValidate(c, &req); err != nil {
		fmt.Println(err == nil)
		return err
	}
	w, err := h.svc.CreateWallet(req.OwnerID, wallet.Currency(req.Currency))
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusCreated, "WALLET_CREATED", "Wallet successfully created", w)
}

// ListWallets godoc
// @Summary      List all wallets for an owner
// @Tags         wallets
// @Produce      json
// @Param        owner_id 	query     string  true  "Owner ID"
// @Success      200       	{object}  response.OKResponse{data=[]wallet.Wallet}
// @Failure      400       	{object}  response.BadRequestResponse{details=[]response.FieldError}
// @Failure      404   		{object}  response.NotFoundResponse
// @Router       /wallets [get]
func (h *Handler) ListWallets(c echo.Context) error {
	var q listWalletsQuery
	if err := c.Bind(&q); err != nil {
		return response.BadRequest(c, "Invalid query parameters")
	}
	if err := c.Validate(&q); err != nil {
		return response.BadRequest(c, "Validation failed for query parameters", parseValidationErrors(err)...)
	}

	wallets, err := h.svc.ListWallets(q.OwnerID)
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "SUCCESS", "Wallets retrieved successfully", wallets)
}

// GetWallet godoc
// @Summary      Get wallet details
// @Tags         wallets
// @Produce      json
// @Param        id   path      string  true  "Wallet ID"
// @Success      200  {object}  response.OKResponse{data=wallet.Wallet}
// @Failure      404   {object}  response.NotFoundResponse
// @Router       /wallets/{id} [get]
func (h *Handler) GetWallet(c echo.Context) error {
	w, err := h.svc.GetWallet(c.Param("id"))
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "SUCCESS", "Wallet retrieved successfully", w)
}

// Topup godoc
// @Summary      Add money to a wallet
// @Tags         wallets
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header    string  true  "Unique idempotency key (UUID)"
// @Param        id    path      string         true  "Wallet ID"
// @Param        body  body      amountRequest  true  "Topup payload"
// @Success      200   {object}  response.OKResponse{data=wallet.Wallet}
// @Failure      400   {object}  response.BadRequestResponse{details=[]response.FieldError}
// @Failure      404   {object}  response.NotFoundResponse
// @Failure      409   {object}  response.ConflictResponse
// @Router       /wallets/{id}/topup [post]
func (h *Handler) Topup(c echo.Context) error {
	idempotencyKey := c.Request().Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		return response.Error(c, wallet.ErrIdempotencyKeyRequired)
	}

	var req amountRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}
	w, err := h.svc.Topup(c.Param("id"), req.Amount, idempotencyKey)
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "TOPUP_SUCCESS", "Topup processed successfully", w)
}

// Pay godoc
// @Summary      Deduct money from a wallet
// @Tags         wallets
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header    string  true  "Unique idempotency key (UUID)"
// @Param        id    path      string         true  "Wallet ID"
// @Param        body  body      amountRequest  true  "Pay payload"
// @Success      200   {object}  response.OKResponse{data=wallet.Wallet}
// @Failure      400   {object}  response.BadRequestResponse{details=[]response.FieldError}
// @Failure      404   {object}  response.NotFoundResponse
// @Failure      409   {object}  response.ConflictResponse
// @Router       /wallets/{id}/pay [post]
func (h *Handler) Pay(c echo.Context) error {
	idempotencyKey := c.Request().Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		return response.Error(c, wallet.ErrIdempotencyKeyRequired)
	}

	var req amountRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}
	w, err := h.svc.Pay(c.Param("id"), req.Amount, idempotencyKey)
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "PAYMENT_SUCCESS", "Payment processed successfully", w)
}

// SuspendWallet godoc
// @Summary      Suspend a wallet
// @Tags         wallets
// @Produce      json
// @Param        id   path      string  true  "Wallet ID"
// @Success      200  {object}  response.OKResponse
// @Failure      404   {object}  response.NotFoundResponse
// @Router       /wallets/{id}/suspend [post]
func (h *Handler) SuspendWallet(c echo.Context) error {
	if err := h.svc.SuspendWallet(c.Param("id")); err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "WALLET_SUSPENDED", "Wallet status updated to suspended", map[string]string{"status": "suspended"})
}

// Transfer godoc
// @Summary      Transfer money between wallets
// @Tags         wallets
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header    string  true  "Unique idempotency key (UUID)"
// @Param        body  body      transferRequest  true  "Transfer payload"
// @Success      200   {object}  response.OKResponse
// @Failure      400   {object}  response.BadRequestResponse{details=[]response.FieldError}
// @Failure      404   {object}  response.NotFoundResponse
// @Failure      409   {object}  response.ConflictResponse
// @Router       /wallets/transfer [post]
func (h *Handler) Transfer(c echo.Context) error {
	idempotencyKey := c.Request().Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		return response.Error(c, wallet.ErrIdempotencyKeyRequired)
	}

	var req transferRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}
	err := h.svc.Transfer(req.FromWalletID, req.ToWalletID, req.Amount, idempotencyKey)
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "TRANSFER_SUCCESS", "Transfer completed successfully", nil)
}

// LedgerHistory godoc
// @Summary      Get the full ledger history for a wallet
// @Tags         wallets
// @Produce      json
// @Param        id   path      string  true  "Wallet ID"
// @Success      200  {object}  response.OKResponse{data=[]wallet.LedgerEntry}
// @Failure      404  {object}  response.NotFoundResponse
// @Router       /wallets/{id}/ledger [get]
func (h *Handler) LedgerHistory(c echo.Context) error {
	entries, err := h.svc.LedgerHistory(c.Param("id"))
	if err != nil {
		return response.Error(c, err)
	}
	return response.Success(c, http.StatusOK, "SUCCESS", "Ledger history retrieved successfully", entries)
}

// ── Helpers & Validation Parsers ─────────────────────────────────────

func bindAndValidate(c echo.Context, v interface{}) error {
	if err := c.Bind(v); err != nil {
		return response.BadRequest(c, "Invalid JSON payload structure")
	}
	if err := c.Validate(v); err != nil {
		return response.BadRequest(c, "Validation failed for one or more fields", parseValidationErrors(err)...)
	}
	return nil
}

// parseValidationErrors menguraikan error validator/v10 menjadi FieldError slice yang bersih
func parseValidationErrors(err error) []response.FieldError {
	var fieldErrors []response.FieldError

	if ve, ok := err.(validator.ValidationErrors); ok {
		for _, fe := range ve {
			issueMsg := fmt.Sprintf("Failed on '%s' validation rule", fe.Tag())
			switch fe.Tag() {
			case "required":
				issueMsg = "Field is required and cannot be empty"
			case "min":
				issueMsg = fmt.Sprintf("Minimum length/value is %s", fe.Param())
			case "max":
				issueMsg = fmt.Sprintf("Maximum length/value is %s", fe.Param())
			case "uuid":
				issueMsg = "Must be a valid UUID format"
			case "numeric":
				issueMsg = "Must be a valid numeric string"
			}

			fieldErrors = append(fieldErrors, response.FieldError{
				Field: fe.Field(),
				Issue: issueMsg,
			})
		}
	} else {
		fieldErrors = append(fieldErrors, response.FieldError{
			Field: "general",
			Issue: err.Error(),
		})
	}

	return fieldErrors
}
