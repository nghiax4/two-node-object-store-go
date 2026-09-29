package storage

import (
	"encoding/binary"

	"go.etcd.io/bbolt"
)

// QueueEntry is one pending job: send the current version of Key to the
// replica. A lower Seq means an older entry.
type QueueEntry struct {
	Seq uint64
	Key string
}

// enqueueTx adds a queue entry for key. It runs inside the caller's
// transaction, so the entry is saved together with whatever else that
// transaction writes.
func enqueueTx(tx *bbolt.Tx, key string) error {
	queue := tx.Bucket(replicationBucket)
	seq, err := queue.NextSequence()
	if err != nil {
		return err
	}
	if err := queue.Put(encodeSeq(seq), []byte(key)); err != nil {
		return err
	}
	return nil
}

// OldestQueued returns the entry with the lowest Seq. ok is false when the
// queue is empty.
func (s *Store) OldestQueued() (entry QueueEntry, ok bool, err error) {
	err = s.db.View(func(tx *bbolt.Tx) error {
		k, v := tx.Bucket(replicationBucket).Cursor().First()
		if k == nil {
			return nil
		}
		// string(v) copies the bytes, so the key stays valid after the
		// transaction ends.
		entry = QueueEntry{Seq: decodeSeq(k), Key: string(v)}
		ok = true
		return nil
	})
	return entry, ok, err
}

// Dequeue removes the entry with this Seq, after a successful send.
// Removing an entry that's already gone is not an error.
func (s *Store) Dequeue(seq uint64) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(replicationBucket).Delete(encodeSeq(seq))
	})
}

// encodeSeq stores seq big-endian, so bbolt's byte-order sorting matches
// numeric order.
func encodeSeq(seq uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, seq)
	return b
}

func decodeSeq(b []byte) uint64 {
	return binary.BigEndian.Uint64(b)
}
