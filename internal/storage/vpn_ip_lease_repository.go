package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// vpnIPLeaseColumns lists every persisted column of vpn_ip_leases in DDL order.
// VPNIPLease.TTL is deliberately absent: it is an input that bounds
// lease_expires_at on write and is derived again on every read.
const vpnIPLeaseColumns = `id,node_id,subnet,allocated_count,lease_holder,lease_expires_at,epoch,acquired_at,updated_at`

// vpnLeaseReleasedExpiry is the instant Release writes into lease_expires_at.
// Any fixed past instant works; the Unix epoch is used because it is obviously
// in the past and survives the VARCHAR(32) timestamp round trip unchanged.
var vpnLeaseReleasedExpiry = time.Unix(0, 0).UTC()

// vpnIPLeaseRetryBudget bounds the compare-and-set loops. Eight attempts match
// leaseRepo.RegisterConnection, which contends on the same kind of row.
const vpnIPLeaseRetryBudget = 8

// errVPNLeaseRetry signals a lost compare-and-set: the row changed underneath the
// transaction, so the whole decision has to be made again against fresh data. It
// never escapes the repository.
var errVPNLeaseRetry = errors.New("vpn ip lease compare-and-set lost a race")

type vpnIPLeaseRepo struct {
	db     *sql.DB
	driver string
}

// NewVPNIPLeaseRepository is retained for SQLite callers. MySQL callers must use
// NewVPNIPLeaseRepositoryWithDriver so row-lock fencing is enabled explicitly.
// Deprecated: use NewVPNIPLeaseRepositoryWithDriver for non-SQLite databases.
func NewVPNIPLeaseRepository(db *sql.DB) VPNIPLeaseRepository {
	return NewVPNIPLeaseRepositoryWithDriver(db, DriverSQLite)
}

// NewVPNIPLeaseRepositoryWithDriver constructs the repository with the driver
// capability needed to pick the correct concurrency strategy.
func NewVPNIPLeaseRepositoryWithDriver(db *sql.DB, driver string) VPNIPLeaseRepository {
	return &vpnIPLeaseRepo{db: db, driver: normalizeDriver(driver)}
}

// vpnIPLeaseTTL applies the same default leaseRepo uses for a connection lease.
// A non-positive TTL would persist a lease that is already expired and therefore
// instantly takeable by any other node, so it is normalised to one minute rather
// than rejected: an absent TTL means "use the default", not "this is a bug".
func vpnIPLeaseTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return time.Minute
	}
	return ttl
}

// validateVPNIPLease enforces the identity fields a lease needs in order to be
// fenced. AllocatedCount and Epoch are only checked for sign because both start
// at zero and are advanced by the repository, never supplied by a caller.
func validateVPNIPLease(lease VPNIPLease) error {
	switch {
	case strings.TrimSpace(lease.NodeID) == "":
		return errors.New("vpn ip lease node is required")
	case strings.TrimSpace(lease.Subnet) == "":
		return errors.New("vpn ip lease subnet is required")
	case strings.TrimSpace(lease.LeaseHolder) == "":
		return errors.New("vpn ip lease holder is required")
	case lease.AllocatedCount < 0:
		return errors.New("vpn ip lease allocated count must not be negative")
	case lease.Epoch < 0:
		return errors.New("vpn ip lease epoch must not be negative")
	}
	return nil
}

// AcquireSubnet claims a /24 for a node. A subnet nobody holds is inserted at
// epoch 1; the current holder may re-acquire a live lease and move the epoch
// forward; anyone else must wait until it expires. Every successful acquire
// advances the epoch, which is what fences a node that lost its lease out of
// Renew, Release and AddAllocated without anybody having to notify it.
func (r *vpnIPLeaseRepo) AcquireSubnet(ctx context.Context, lease VPNIPLease) (VPNIPLease, error) {
	if err := validateVPNIPLease(lease); err != nil {
		return VPNIPLease{}, err
	}
	ttl := vpnIPLeaseTTL(lease.TTL)
	for attempt := 0; attempt < vpnIPLeaseRetryBudget; attempt++ {
		now := time.Now().UTC()
		acquired, err := r.tryAcquireSubnet(ctx, lease, now, now.Add(ttl))
		if err == nil {
			return acquired, nil
		}
		if !errors.Is(err, errVPNLeaseRetry) {
			return VPNIPLease{}, err
		}
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	return VPNIPLease{}, errors.New("vpn ip subnet lease contention exceeded retry budget")
}

// tryAcquireSubnet makes one acquisition attempt. Expiry is compared in Go
// rather than in the WHERE clause because RFC3339Nano trims trailing zeros, so
// stored timestamps are not reliably ordered as strings.
func (r *vpnIPLeaseRepo) tryAcquireSubnet(ctx context.Context, lease VPNIPLease, now, expires time.Time) (VPNIPLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return VPNIPLease{}, err
	}
	current, err := r.selectLease(ctx, tx, lease.NodeID, lease.Subnet)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return VPNIPLease{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		// A subnet nobody has leased cannot have handed out addresses, so the
		// counter starts at zero whatever the caller passed in.
		id := newID("vpn-ip-lease")
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO vpn_ip_leases(`+vpnIPLeaseColumns+`) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, lease.NodeID, lease.Subnet, 0, lease.LeaseHolder, tm(expires), int64(1), tm(now), tm(now)); insertErr != nil {
			_ = tx.Rollback()
			if isDuplicateError(insertErr) {
				return VPNIPLease{}, errVPNLeaseRetry
			}
			return VPNIPLease{}, insertErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return VPNIPLease{}, commitErr
		}
		return vpnIPLeaseAcquired(id, lease, 0, 1, now, expires), nil
	}
	if current.LeaseHolder != lease.LeaseHolder && current.LeaseExpiresAt.After(now) {
		_ = tx.Rollback()
		return VPNIPLease{}, ErrVPNIPLeaseHeld
	}
	nextEpoch := current.Epoch + 1
	// allocated_count is deliberately absent from the SET list: a takeover
	// inherits the addresses already handed out on this subnet, because the
	// peers holding them are still connected to it.
	res, updateErr := tx.ExecContext(ctx, `UPDATE vpn_ip_leases SET lease_holder=?,lease_expires_at=?,epoch=?,acquired_at=?,updated_at=? WHERE node_id=? AND subnet=? AND epoch=?`,
		lease.LeaseHolder, tm(expires), nextEpoch, tm(now), tm(now), lease.NodeID, lease.Subnet, current.Epoch)
	if updateErr != nil {
		_ = tx.Rollback()
		return VPNIPLease{}, updateErr
	}
	affected, affectedErr := res.RowsAffected()
	if affectedErr != nil {
		_ = tx.Rollback()
		return VPNIPLease{}, affectedErr
	}
	if affected != 1 {
		// Another holder won between the read and the write. The epoch in the
		// WHERE clause is the fence, so retrying against the new row is the only
		// correct answer; guessing here would let two nodes share a /24.
		_ = tx.Rollback()
		return VPNIPLease{}, errVPNLeaseRetry
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return VPNIPLease{}, commitErr
	}
	return vpnIPLeaseAcquired(current.ID, lease, current.AllocatedCount, nextEpoch, now, expires), nil
}

// vpnIPLeaseAcquired builds the value returned to a successful caller. TTL is
// derived the same way reads derive it, so a caller can compare what it asked
// for with what it got without issuing a second query.
func vpnIPLeaseAcquired(id string, lease VPNIPLease, allocated int, epoch int64, now, expires time.Time) VPNIPLease {
	return VPNIPLease{
		ID:             id,
		NodeID:         lease.NodeID,
		Subnet:         lease.Subnet,
		AllocatedCount: allocated,
		LeaseHolder:    lease.LeaseHolder,
		LeaseExpiresAt: expires,
		Epoch:          epoch,
		AcquiredAt:     now,
		UpdatedAt:      now,
		TTL:            time.Until(expires),
	}
}

// Renew buys more time on a lease the caller still owns. It never moves the
// epoch, because doing so would fence the renewing node out of its own lease,
// and it never touches allocated_count.
func (r *vpnIPLeaseRepo) Renew(ctx context.Context, nodeID, subnet, holder string, epoch int64, ttl time.Duration) error {
	if strings.TrimSpace(holder) == "" {
		return errors.New("vpn ip lease holder is required")
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE vpn_ip_leases SET lease_expires_at=?,updated_at=? WHERE node_id=? AND subnet=? AND lease_holder=? AND epoch=?`,
		tm(now.Add(vpnIPLeaseTTL(ttl))), tm(now), nodeID, subnet, holder, epoch)
	if err != nil {
		return err
	}
	return r.fenceResult(ctx, res, nodeID, subnet)
}

// Release gives the subnet back without deleting the row: peers that already
// hold an address on this subnet outlive the lease that allocated it, and the
// row is the audit trail for that. The releasing node stays the holder of record
// until a successor takes over, so an in-flight AddAllocated can still land.
// That window is acceptable because the authoritative guarantee against handing
// one address to two peers is vpn_peers.UNIQUE(node_id, vpn_ip), not this
// counter.
func (r *vpnIPLeaseRepo) Release(ctx context.Context, nodeID, subnet, holder string, epoch int64) error {
	if strings.TrimSpace(holder) == "" {
		return errors.New("vpn ip lease holder is required")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE vpn_ip_leases SET lease_expires_at=?,updated_at=? WHERE node_id=? AND subnet=? AND lease_holder=? AND epoch=?`,
		tm(vpnLeaseReleasedExpiry), tm(time.Now().UTC()), nodeID, subnet, holder, epoch)
	if err != nil {
		return err
	}
	return r.fenceResult(ctx, res, nodeID, subnet)
}

// AddAllocated moves the derived counter and returns the value it wrote. It runs
// inside a transaction so the returned number is the number that was stored, and
// so the "must not go negative" rule is decided against a locked row rather than
// a value another node may already have changed.
func (r *vpnIPLeaseRepo) AddAllocated(ctx context.Context, nodeID, subnet, holder string, epoch int64, delta int) (int, error) {
	if strings.TrimSpace(holder) == "" {
		return 0, errors.New("vpn ip lease holder is required")
	}
	for attempt := 0; attempt < vpnIPLeaseRetryBudget; attempt++ {
		next, err := r.tryAddAllocated(ctx, nodeID, subnet, holder, epoch, delta)
		if err == nil {
			return next, nil
		}
		if !errors.Is(err, errVPNLeaseRetry) {
			return 0, err
		}
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	return 0, errors.New("vpn ip lease counter contention exceeded retry budget")
}

func (r *vpnIPLeaseRepo) tryAddAllocated(ctx context.Context, nodeID, subnet, holder string, epoch int64, delta int) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	current, err := r.selectLease(ctx, tx, nodeID, subnet)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if current.Epoch != epoch || current.LeaseHolder != holder {
		_ = tx.Rollback()
		return 0, ErrVPNIPLeaseStaleEpoch
	}
	next := current.AllocatedCount + delta
	if next < 0 {
		// Releasing an address that was never allocated would make the subnet
		// look like it has capacity it does not have, which is how one address
		// ends up configured on two peers.
		_ = tx.Rollback()
		return 0, fmt.Errorf("vpn ip lease %s on node %s holds %d addresses and cannot release %d", subnet, nodeID, current.AllocatedCount, -delta)
	}
	res, err := tx.ExecContext(ctx, `UPDATE vpn_ip_leases SET allocated_count=?,updated_at=? WHERE node_id=? AND subnet=? AND epoch=?`,
		next, tm(time.Now().UTC()), nodeID, subnet, epoch)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if affected != 1 {
		_ = tx.Rollback()
		return 0, errVPNLeaseRetry
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func (r *vpnIPLeaseRepo) Get(ctx context.Context, nodeID, subnet string) (VPNIPLease, error) {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(subnet) == "" {
		return VPNIPLease{}, sql.ErrNoRows
	}
	return scanVPNIPLease(r.db.QueryRowContext(ctx, `SELECT `+vpnIPLeaseColumns+` FROM vpn_ip_leases WHERE node_id=? AND subnet=?`, nodeID, subnet))
}

// ListByHolder returns every subnet a node holds, ordered by subnet so two
// consecutive reads can be diffed without re-sorting. An unknown holder is an
// empty slice rather than an error, because a node that has never leased a
// subnet asks this on every start.
func (r *vpnIPLeaseRepo) ListByHolder(ctx context.Context, holder string) ([]VPNIPLease, error) {
	if strings.TrimSpace(holder) == "" {
		return []VPNIPLease{}, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+vpnIPLeaseColumns+` FROM vpn_ip_leases WHERE lease_holder=? ORDER BY subnet`, holder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	leases := []VPNIPLease{}
	for rows.Next() {
		lease, err := scanVPNIPLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return leases, nil
}

// selectLease reads one lease inside a transaction, taking a row lock on MySQL.
// SQLite has no SELECT ... FOR UPDATE and relies on the epoch compare-and-set in
// the following UPDATE instead, which is why the lock is dialect specific.
func (r *vpnIPLeaseRepo) selectLease(ctx context.Context, tx *sql.Tx, nodeID, subnet string) (VPNIPLease, error) {
	query := `SELECT ` + vpnIPLeaseColumns + ` FROM vpn_ip_leases WHERE node_id=? AND subnet=?`
	if r.driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	return scanVPNIPLease(tx.QueryRowContext(ctx, query, nodeID, subnet))
}

// fenceResult turns a fenced UPDATE result into a precise error. Zero rows means
// either the lease is gone or the caller's holder and epoch no longer match; one
// extra read tells the two apart, so a caller can distinguish "re-acquire the
// subnet" from "this subnet was never leased". The read is part of the report,
// not part of the fence.
func (r *vpnIPLeaseRepo) fenceResult(ctx context.Context, res sql.Result, nodeID, subnet string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	if _, err := r.Get(ctx, nodeID, subnet); err != nil {
		return err
	}
	return ErrVPNIPLeaseStaleEpoch
}

func scanVPNIPLease(row rowScanner) (VPNIPLease, error) {
	var lease VPNIPLease
	var allocated, epoch sql.NullInt64
	var expires, acquired, updated sql.NullString
	if err := row.Scan(&lease.ID, &lease.NodeID, &lease.Subnet, &allocated, &lease.LeaseHolder,
		&expires, &epoch, &acquired, &updated); err != nil {
		return VPNIPLease{}, err
	}
	lease.AllocatedCount = int(allocated.Int64)
	lease.Epoch = epoch.Int64
	lease.LeaseExpiresAt = parseTime(expires.String)
	lease.AcquiredAt = parseTime(acquired.String)
	lease.UpdatedAt = parseTime(updated.String)
	// TTL is not persisted; reads derive what is left so a caller can decide
	// whether to renew without parsing the expiry itself.
	if !lease.LeaseExpiresAt.IsZero() {
		lease.TTL = time.Until(lease.LeaseExpiresAt)
	}
	return lease, nil
}
