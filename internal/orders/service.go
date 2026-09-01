package orders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

// business logic lives here

var (
	ErrOutOfStock      = errors.New("product is out of stock")
	ErrOrderNotPending = errors.New("order is not pending")
	ErrForbidden       = errors.New("caller does not own this order")

	// ErrPaidButNotFulfillable means a payment succeeded for an order that is
	// no longer in a state that can consume it — almost always one the expiry
	// sweeper already released. Money moved but no stock is owed, so it needs
	// a refund. Surfaced rather than swallowed on purpose.
	ErrPaidButNotFulfillable = errors.New("payment succeeded for an order that is no longer awaiting payment")
)

// PaymentGateway is the half of checkout that talks to a payment processor.
// Declared here, as an interface, rather than importing a concrete Stripe
// client: this package owns the order lifecycle and should not depend on who
// moves the money. internal/payments provides the Stripe implementation.
type PaymentGateway interface {
	CreatePaymentIntent(ctx context.Context, p PaymentIntentParams) (PaymentIntent, error)
}

type PaymentIntentParams struct {
	OrderID       int64
	AmountInCents int64
	CustomerEmail string
}

type PaymentIntent struct {
	ID           string
	ClientSecret string
}

// CheckoutResult is what the synchronous half of checkout hands back. The
// client secret is what the frontend confirms the payment with; the order is
// NOT complete at this point and its stock is still only reserved.
type CheckoutResult struct {
	Order           repo.Order
	PaymentIntentID string
	ClientSecret    string
}

type Service interface {
	GetOrder(ctx context.Context, id int64, claims auth.Claims) (repo.FindOrderByIDRow, error)
	ListOrders(ctx context.Context, claims auth.Claims, limit, offset int32) ([]repo.ListOrdersByCustomerRow, error)
	CreateOrder(ctx context.Context, productID int64, claims auth.Claims) (repo.Order, error)
	Checkout(ctx context.Context, orderID int64, claims auth.Claims) (CheckoutResult, error)
	CancelOrder(ctx context.Context, orderID int64, claims auth.Claims) (repo.Order, error)

	// Driven by the Stripe webhook worker, not by an HTTP caller.
	FulfillPayment(ctx context.Context, paymentIntentID string) error
	FailPayment(ctx context.Context, paymentIntentID string) error

	// Driven by the expiry sweeper.
	SweepExpired(ctx context.Context, batchSize int32) (int, error)
}

// implements Service interface
type svc struct {
	repo    repo.Querier
	db      repo.Beginner
	gateway PaymentGateway

	// reservationTTL bounds how long an unpaid order may hold stock. It is
	// our number, not Stripe's: if the processor never calls back, the unit
	// still has to come back on sale.
	reservationTTL time.Duration
}

func NewService(repo repo.Querier, db repo.Beginner, gateway PaymentGateway, reservationTTL time.Duration) Service {
	return &svc{
		repo:           repo,
		db:             db,
		gateway:        gateway,
		reservationTTL: reservationTTL,
	}
}

// owns reports whether the caller identified by claims owns the order.
//
// Prefers the Cognito sub, which never changes, and falls back to email only
// for customer rows created before cognito_sub existed. Those backfill on the
// owner's next GET /me, after which the email path stops being reachable.
func owns(customerSub pgtype.Text, customerEmail string, claims auth.Claims) bool {
	if customerSub.Valid && claims.Subject != "" {
		return customerSub.String == claims.Subject
	}
	return customerEmail != "" && customerEmail == claims.Email
}

func (s *svc) GetOrder(ctx context.Context, id int64, claims auth.Claims) (repo.FindOrderByIDRow, error) {
	order, err := s.repo.FindOrderByID(ctx, id)
	if err != nil {
		return repo.FindOrderByIDRow{}, err
	}
	if !owns(order.CustomerCognitoSub, order.CustomerEmail, claims) {
		return repo.FindOrderByIDRow{}, ErrForbidden
	}
	return order, nil
}

// ListOrders returns the caller's own orders. It resolves the caller to a
// customer via the same upsert GET /me uses, so listing works even for a user
// who has signed up but never ordered (they get an empty list, not a 404).
func (s *svc) ListOrders(ctx context.Context, claims auth.Claims, limit, offset int32) ([]repo.ListOrdersByCustomerRow, error) {
	customer, err := s.linkCustomer(ctx, s.repo, claims)
	if err != nil {
		return nil, err
	}

	return s.repo.ListOrdersByCustomer(ctx, repo.ListOrdersByCustomerParams{
		CustomerID: customer.ID,
		Limit:      limit,
		Offset:     offset,
	})
}

func (s *svc) linkCustomer(ctx context.Context, q repo.Querier, claims auth.Claims) (repo.Customer, error) {
	return q.LinkCustomer(ctx, repo.LinkCustomerParams{
		Email:      claims.Email,
		CognitoSub: pgtype.Text{String: claims.Subject, Valid: claims.Subject != ""},
	})
}

// CreateOrder is the reserve half of the buy flow: it finds/creates the
// customer, snapshots the price, reserves one unit of stock, and creates a
// 'pending' order, all in one transaction.
//
// Statement order matters. ReserveStock takes a row lock on the contended
// stock row, and everything after it in the transaction extends how long that
// lock is held — so the customer upsert and the price read deliberately run
// first, and only the order insert follows the reservation.
func (s *svc) CreateOrder(ctx context.Context, productID int64, claims auth.Claims) (repo.Order, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repo.Order{}, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	customer, err := s.linkCustomer(ctx, q, claims)
	if err != nil {
		return repo.Order{}, err
	}

	product, err := q.FindProductByID(ctx, productID)
	if err != nil {
		return repo.Order{}, err
	}

	if _, err := q.ReserveStock(ctx, productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Order{}, ErrOutOfStock
		}
		return repo.Order{}, err
	}

	order, err := q.CreateOrder(ctx, repo.CreateOrderParams{
		CustomerID:   customer.ID,
		ProductID:    productID,
		TotalInCents: product.PriceInCents,
		ExpiresAt:    pgtype.Timestamptz{Time: time.Now().Add(s.reservationTTL), Valid: true},
	})
	if err != nil {
		return repo.Order{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return repo.Order{}, err
	}

	return order, nil
}

// Checkout is the synchronous half of payment. It does NOT complete the order
// or commit stock — a confirmed payment does, asynchronously, via the webhook
// worker.
//
// The gateway call sits deliberately between two short transactions rather
// than inside one. An outbound HTTP request holding a row lock on contended
// inventory is exactly the pathology this project exists to avoid: Stripe's
// latency would become inventory lock latency for every other buyer.
func (s *svc) Checkout(ctx context.Context, orderID int64, claims auth.Claims) (CheckoutResult, error) {
	existing, err := s.repo.FindOrderByID(ctx, orderID)
	if err != nil {
		return CheckoutResult{}, err
	}
	if !owns(existing.CustomerCognitoSub, existing.CustomerEmail, claims) {
		return CheckoutResult{}, ErrForbidden
	}
	if existing.Status != StatusPending {
		return CheckoutResult{}, ErrOrderNotPending
	}

	// No transaction is open across this call.
	intent, err := s.gateway.CreatePaymentIntent(ctx, PaymentIntentParams{
		OrderID:       existing.ID,
		AmountInCents: int64(existing.TotalInCents),
		CustomerEmail: existing.CustomerEmail,
	})
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("create payment intent for order %d: %w", orderID, err)
	}

	// Guarded on 'pending', so if another checkout raced us here it wins and
	// we report a conflict rather than attaching a second intent. The loser's
	// intent is simply never confirmed and expires at Stripe.
	order, err := s.repo.MarkOrderAwaitingPayment(ctx, repo.MarkOrderAwaitingPaymentParams{
		ID:                    orderID,
		StripePaymentIntentID: pgtype.Text{String: intent.ID, Valid: true},
		ExpiresAt:             pgtype.Timestamptz{Time: time.Now().Add(s.reservationTTL), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CheckoutResult{}, ErrOrderNotPending
		}
		return CheckoutResult{}, err
	}

	return CheckoutResult{
		Order:           order,
		PaymentIntentID: intent.ID,
		ClientSecret:    intent.ClientSecret,
	}, nil
}

// CancelOrder releases a reservation the buyer no longer wants.
//
// Only 'pending' orders can be cancelled. Once an order is awaiting payment a
// PaymentIntent exists and the buyer may still confirm it, so cancelling would
// open a paid-but-cancelled hole identical to the paid-but-expired one. Those
// orders are left to the sweeper instead.
func (s *svc) CancelOrder(ctx context.Context, orderID int64, claims auth.Claims) (repo.Order, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repo.Order{}, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	existing, err := q.FindOrderByID(ctx, orderID)
	if err != nil {
		return repo.Order{}, err
	}
	if !owns(existing.CustomerCognitoSub, existing.CustomerEmail, claims) {
		return repo.Order{}, ErrForbidden
	}

	order, err := q.CancelPendingOrder(ctx, orderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Order{}, ErrOrderNotPending
		}
		return repo.Order{}, err
	}

	if _, err := q.ReleaseStock(ctx, order.ProductID); err != nil {
		return repo.Order{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return repo.Order{}, err
	}

	return order, nil
}

// FulfillPayment is the asynchronous half: a confirmed payment completes the
// order and converts its reservation into a sale.
//
// Safe to call more than once for the same intent. Stripe delivers webhooks
// at-least-once, and CompleteOrder's status guard is what makes a redelivery
// a no-op instead of a second stock commit.
func (s *svc) FulfillPayment(ctx context.Context, paymentIntentID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	existing, err := q.FindOrderByPaymentIntentID(ctx, pgtype.Text{String: paymentIntentID, Valid: true})
	if err != nil {
		return err
	}

	order, err := q.CompleteOrder(ctx, existing.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The order is not awaiting payment. Very different cases hide
			// behind the same zero-row result: a benign webhook redelivery
			// for an order already completed, versus a payment that landed
			// after the sweeper released the stock.
			return classifyUnfulfillable(existing)
		}
		return err
	}

	if _, err := q.CommitStock(ctx, order.ProductID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// FailPayment releases the reservation behind a payment that did not succeed.
// Guarded the same way as fulfilment, so a redelivered failure releases stock
// at most once.
func (s *svc) FailPayment(ctx context.Context, paymentIntentID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	existing, err := q.FindOrderByPaymentIntentID(ctx, pgtype.Text{String: paymentIntentID, Valid: true})
	if err != nil {
		return err
	}

	order, err := q.FailOrder(ctx, existing.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already failed, expired, or completed — nothing left to release.
			return nil
		}
		return err
	}

	if _, err := q.ReleaseStock(ctx, order.ProductID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SweepExpired marks timed-out reservations expired and puts their stock back
// on sale, returning how many it released.
//
// Both statements run in one transaction so an order can never be recorded as
// expired without its unit being returned. ExpireOrders uses FOR UPDATE SKIP
// LOCKED, so running this from a second process is safe.
func (s *svc) SweepExpired(ctx context.Context, batchSize int32) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	expired, err := q.ExpireOrders(ctx, batchSize)
	if err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return 0, nil
	}

	for _, order := range expired {
		if _, err := q.ReleaseStock(ctx, order.ProductID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	return len(expired), nil
}
