package state

import (
	"context"
	"encoding/json"
	"time"
)

const StateStoppingRecord = "stopping"

// NarrowExpiredInstance narrows the authorization of ONE instance inside the
// database and never touches anything else.
//
// It exists for the fail-closed expiry fallback: that path must enforce a
// delivered expiry even when the apply serialization gate is busy, but it must
// not overwrite the record of an apply that committed in the meantime. So the
// update is a single statement that
//
//   - only rewrites the user set, the applied count, the expiry trigger, the
//     state/error text and the timestamp,
//   - always keeps the recorded configuration and core generation (the recovery
//     data of a core that may still be alive), and
//   - only applies while the record still has the exact user set that was read
//     and while it is not an already finished tombstone. A concurrent apply that
//     committed a different authorization makes the guard fail, and the caller
//     simply leaves the newer record alone.
//
// It reports whether the row was narrowed.
func (s *Store) NarrowExpiredInstance(ctx context.Context, rec InstanceRecord) (bool, error) {
	usersRaw, err := json.Marshal(rec.Users)
	if err != nil {
		return false, err
	}
	previousRaw, err := json.Marshal(rec.UsersBefore)
	if err != nil {
		return false, err
	}
	next := int64(0)
	if !rec.NextExpiry.IsZero() {
		next = rec.NextExpiry.UTC().UnixMilli()
	}
	updated := rec.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE instances
		SET users=?, applied_user_count=?, next_expiry=?, state=?, error=?, updated_at=?
		WHERE node_id=? AND users=? AND state <> ?`,
		string(usersRaw), rec.AppliedUserCount, next, rec.State, rec.Error, updated.UTC().UnixMilli(),
		rec.NodeID, string(previousRaw), StateStoppingRecord)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
