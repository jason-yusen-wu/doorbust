package orders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
)

// ErrIdempotencyKeyReused means a key was replayed with a different request
// body. Almost always a client bug, and returning the first order instead would
// quietly hand the caller a product they did not ask for.
var ErrIdempotencyKeyReused = errors.New("idempotency key already used with a different request")

// ErrReserveInProgress means another request holding the same key is still
// running. Retriable — unlike the reuse error — so it is a distinct sentinel.
var ErrReserveInProgress = errors.New("a request with this idempotency key is still in progress")

// idempotentStrategy makes POST /orders safe to retry.
//
// It wraps any arm rather than being one, so idempotency and the contention
// experiment stay independent: every arm can be measured with and without it,
// and the cost of the extra statement is a number rather than an assumption.
//
// The claim is a guarded INSERT, the same idiom as ReserveStock: it either wins
// or matches zero rows, and zero rows means a concurrent caller got there
// first. No read-then-write, so two simultaneous retries cannot both proceed.
//
// The extra statement is on the hot path but NOT inside the critical section —
// it touches a different table, keyed on the caller, so it never contends with
// another buyer for the same SKU.
type idempotentStrategy struct {
	inner ReserveStrategy
	repo  repo.Querier
}

// WithIdempotency wraps an arm so repeated requests carrying the same
// Idempotency-Key reserve at most one unit.
func WithIdempotency(inner ReserveStrategy, q repo.Querier) ReserveStrategy {
	return &idempotentStrategy{inner: inner, repo: q}
}

func (s *idempotentStrategy) Name() string { return s.inner.Name() + "+idempotency" }

func (s *idempotentStrategy) Reserve(ctx context.Context, p ReserveParams) (repo.Order, error) {
	// No key means the caller has not opted in, and the endpoint behaves as it
	// always has. Requiring one would break every existing client.
	//
	// A key is also meaningless without a stable identity to scope it to:
	// scoping by key alone would let one caller's key collide with another's.
	if p.IdempotencyKey == "" || p.Claims.Subject == "" {
		return s.inner.Reserve(ctx, p)
	}

	hash := requestHash(p.ProductID)

	claimed, err := s.repo.ClaimIdempotencyKey(ctx, repo.ClaimIdempotencyKeyParams{
		CognitoSub:  p.Claims.Subject,
		IdemKey:     p.IdempotencyKey,
		RequestHash: hash,
	})
	switch {
	case err == nil:
		// We own the key, so we are the one request that actually reserves.
		return s.reserveAndRecord(ctx, p, claimed)
	case !errors.Is(err, pgx.ErrNoRows):
		return repo.Order{}, err
	}

	// Someone else holds the key: this is a retry, a double-submit, or a
	// different request reusing the key by mistake.
	return s.replay(ctx, p, hash)
}

func (s *idempotentStrategy) reserveAndRecord(ctx context.Context, p ReserveParams, claimed repo.OrderIdempotency) (repo.Order, error) {
	order, err := s.inner.Reserve(ctx, p)
	if err != nil {
		// The reserve failed, so the key produced no order. Releasing it lets
		// the caller retry with the same key — holding it would turn a
		// transient failure into a permanent one for that key, which is the
		// opposite of what idempotency is for.
		//
		// Best-effort: if the release fails the key is merely stuck in-progress
		// until it is pruned, which is strictly safer than losing the reserve
		// error the caller actually needs to see.
		_ = s.repo.ReleaseIdempotencyKey(ctx, repo.ReleaseIdempotencyKeyParams{
			CognitoSub: claimed.CognitoSub,
			IdemKey:    claimed.IdemKey,
		})
		return repo.Order{}, err
	}

	if err := s.repo.CompleteIdempotencyKey(ctx, repo.CompleteIdempotencyKeyParams{
		CognitoSub: claimed.CognitoSub,
		IdemKey:    claimed.IdemKey,
		OrderID:    pgtype.Int8{Int64: order.ID, Valid: true},
	}); err != nil {
		// The order exists and the caller should be told so. Failing here would
		// hide a successful reservation behind an error, and the caller's
		// natural response — retry — would then reserve a second unit, which is
		// exactly the bug this code exists to prevent.
		return order, nil
	}

	return order, nil
}

func (s *idempotentStrategy) replay(ctx context.Context, p ReserveParams, hash string) (repo.Order, error) {
	existing, err := s.repo.FindIdempotencyKey(ctx, repo.FindIdempotencyKeyParams{
		CognitoSub: p.Claims.Subject,
		IdemKey:    p.IdempotencyKey,
	})
	if err != nil {
		// The winner released the key between our failed claim and this read,
		// which means their reserve failed. Tell the caller to retry rather
		// than inventing an answer.
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Order{}, ErrReserveInProgress
		}
		return repo.Order{}, err
	}

	if existing.RequestHash != hash {
		return repo.Order{}, ErrIdempotencyKeyReused
	}
	if !existing.OrderID.Valid {
		// Claimed but not finished: the first request is still in flight.
		// Distinct from "here is your order", and must not be collapsed into
		// it — answering 201 with no order would be a lie.
		return repo.Order{}, ErrReserveInProgress
	}

	return s.repo.FindOrderByIDPlain(ctx, existing.OrderID.Int64)
}

// requestHash fingerprints the parts of the request that must match on a
// replay. Only product_id today, because that is the whole body; it is hashed
// rather than stored raw so the column does not grow with the request.
func requestHash(productID int64) string {
	sum := sha256.Sum256([]byte("product_id=" + strconv.FormatInt(productID, 10)))
	return hex.EncodeToString(sum[:])
}
