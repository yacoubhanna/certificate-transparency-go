package gossip

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

const schema = `
        CREATE TABLE IF NOT EXISTS sths (
                version     INTEGER NOT NULL,
                tree_size   INTEGER NOT NULL,
                timestamp   INTEGER NOT NULL,
                root_hash   BYTES NOT NULL,
                signature   BYTES NOT NULL,
                log_id      BYTES NOT NULL,
                PRIMARY KEY (version, tree_size, timestamp, root_hash, log_id)
        );

        CREATE TABLE IF NOT EXISTS scts (
                sct_id  INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
                sct     BYTES NOT NULL UNIQUE
        );

        CREATE TABLE IF NOT EXISTS chains (
                chain_id    INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
                chain       STRING NOT NULL UNIQUE
        );

        CREATE TABLE IF NOT EXISTS sct_feedback (
                chain_id    INTEGER NOT NULL REFERENCES chains(chain_id),
                sct_id      INTEGER NOT NULL REFERENCES scts(sct_id),
                PRIMARY KEY (chain_id, sct_id)

        );`

const insertChain = `INSERT INTO chains(chain) VALUES ($1);`
const insertSCT = `INSERT INTO scts(sct) VALUES ($1);`
const insertSCTFeedback = `INSERT INTO sct_feedback(chain_id, sct_id) VALUES ($1, $2);`
const insertSTHPollination = `INSERT INTO sths(version, tree_size, timestamp, root_hash, signature, log_id) VALUES($1, $2, $3, $4, $5, $6);`

const selectChainID = `SELECT chain_id FROM chains WHERE chain = $1;`

// Selects at most $2 rows from the sths table whose timestamp is newer than $1.
const selectRandomRecentPollination = `SELECT version, tree_size, timestamp, root_hash, signature, log_id FROM sths 
                                          WHERE timestamp >= $1 ORDER BY random() LIMIT $2;`
const selectSCTID = `SELECT sct_id FROM scts WHERE sct = $1;`

const selectNumSCTs = `SELECT COUNT(*) FROM scts;`
const selectNumChains = `SELECT COUNT(*) FROM chains;`
const selectNumFeedback = `SELECT COUNT(*) FROM sct_feedback;`
const selectNumSTHs = `SELECT COUNT(*) FROM sths;`

const selectFeedback = `SELECT COUNT(*) FROM sct_feedback WHERE chain_id = $1 AND sct_id = $2;`
const selectSTH = `SELECT COUNT(*) FROM sths WHERE version = $1 AND tree_size = $2 AND timestamp = $3 AND root_hash = $4 AND signature = $5 AND log_id = $6;`

// Storage provides an SQLite3-backed method for persisting gossip data
type Storage struct {
	db                            *sql.DB
	dbPath                        string
	insertChain                   *sql.Stmt
	insertSCT                     *sql.Stmt
	insertSCTFeedback             *sql.Stmt
	insertSTHPollination          *sql.Stmt
	selectChainID                 *sql.Stmt
	selectRandomRecentPollination *sql.Stmt
	selectSCTID                   *sql.Stmt

	selectNumChains   *sql.Stmt
	selectNumFeedback *sql.Stmt
	selectNumSCTs     *sql.Stmt
	selectNumSTHs     *sql.Stmt

	selectFeedback *sql.Stmt
	selectSTH      *sql.Stmt
}

type statementSQLPair struct {
	Statement **sql.Stmt
	SQL       string
}

func prepareStatement(db *sql.DB, s statementSQLPair) error {
	stmt, err := db.Prepare(s.SQL)
	if err != nil {
		return err
	}
	*(s.Statement) = stmt
	return nil
}

// Open opens the underlying persistent data store.
// Should be called before attempting to use any of the store or search methods.
func (s *Storage) Open(dbPath string) error {
	var err error
	if s.db != nil {
		return errors.New("attempting to call Open() on an already Open()'d Storage")
	}
	if len(dbPath) == 0 {
		return errors.New("attempting to call Open() with an empty file name")
	}
	s.dbPath = dbPath
	s.db, err = sql.Open("sqlite3", s.dbPath)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	for _, p := range []statementSQLPair{
		{&s.insertChain, insertChain},
		{&s.insertSCT, insertSCT},
		{&s.insertSCTFeedback, insertSCTFeedback},
		{&s.insertSTHPollination, insertSTHPollination},
		{&s.selectChainID, selectChainID},
		{&s.selectRandomRecentPollination, selectRandomRecentPollination},
		{&s.selectSCTID, selectSCTID},
		{&s.selectNumChains, selectNumChains},
		{&s.selectNumFeedback, selectNumFeedback},
		{&s.selectNumSCTs, selectNumSCTs},
		{&s.selectNumSTHs, selectNumSTHs},
		{&s.selectFeedback, selectFeedback},
		{&s.selectSTH, selectSTH}} {
		if err := prepareStatement(s.db, p); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the underlying DB storage.
func (s *Storage) Close() error {
	return s.db.Close()
}

func selectThingID(getID *sql.Stmt, thing interface{}) (int64, error) {
	rows, err := getID.Query(thing)
	if err != nil {
		return -1, err
	}
	if !rows.Next() {
		return -1, fmt.Errorf("couldn't look up ID for %v", thing)
	}
	var id int64
	if err = rows.Scan(&id); err != nil {
		return -1, err
	}
	return id, nil
}

// insertThingOrSelectID will attempt to execute the insert Statement (under transaction tx), if that fails due to
// a unique primary key constraint, it will look up that primary key by executing the getID Statement.
// Returns the ID associated with persistent thing, or an error describing the failure.
func insertThingOrSelectID(tx *sql.Tx, insert *sql.Stmt, getID *sql.Stmt, thing interface{}) (int64, error) {
	txInsert := tx.Stmt(insert)
	txGetID := tx.Stmt(getID)
	r, err := txInsert.Exec(thing)
	if err != nil {
		switch e := err.(type) {
		case sqlite3.Error:
			if e.Code == sqlite3.ErrConstraint {
				return selectThingID(txGetID, thing)
			}
		}
		return -1, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return -1, err
	}
	return id, nil
}

func (s *Storage) addChainIfNotExists(tx *sql.Tx, chain []string) (int64, error) {
	flatChain := strings.Join(chain, "")
	return insertThingOrSelectID(tx, s.insertChain, s.selectChainID, flatChain)
}

func (s *Storage) addSCTIfNotExists(tx *sql.Tx, sct string) (int64, error) {
	return insertThingOrSelectID(tx, s.insertSCT, s.selectSCTID, sct)
}

func (s *Storage) addSCTFeedbackIfNotExists(tx *sql.Tx, chainID, sctID int64) error {
	stmt := tx.Stmt(s.insertSCTFeedback)
	_, err := stmt.Exec(chainID, sctID)
	if err != nil {
		switch err.(type) {
		case sqlite3.Error:
			// If this is a dupe that's fine, no need to return an error
			if err.(sqlite3.Error).Code != sqlite3.ErrConstraint {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// AddSCTFeedback stores the passed in feedback object.
func (s *Storage) AddSCTFeedback(feedback SCTFeedback) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// If we return a non-nil error, then rollback the transaction.
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	for _, f := range feedback.Feedback {
		chainID, err := s.addChainIfNotExists(tx, f.X509Chain)
		if err != nil {
			return err
		}
		for _, sct := range f.SCTData {
			sctID, err := s.addSCTIfNotExists(tx, sct)
			if err != nil {
				return err
			}
			if err = s.addSCTFeedbackIfNotExists(tx, chainID, sctID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Storage) addSTHPollinationEntryIfNotExists(tx *sql.Tx, pe STHPollinationEntry) error {
	stmt := tx.Stmt(s.insertSTHPollination)
	_, err := stmt.Exec(pe.STHVersion, pe.TreeSize, pe.Timestamp, pe.Sha256RootHashB64, pe.TreeHeadSignatureB64, pe.LogID)
	if err != nil {
		switch err.(type) {
		case sqlite3.Error:
			// If this is a dupe that's fine, no need to return an error
			if err.(sqlite3.Error).Code != sqlite3.ErrConstraint {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// GetRandomSTHPollination returns a random selection of "fresh" (i.e. at most 14 days old) STHs from the pool.
func (s *Storage) GetRandomSTHPollination(limit int) (*STHPollination, error) {
	freshTime := time.Now().AddDate(0, 0, -14)
	// Occasionally this fails to select the pollen which was added by the
	// AddSTHPollination request which went on trigger this query, even though
	// the transaction committed successfully.  Attempting this query under a
	// transaction doesn't fix it. /sadface
	// Still, that shouldn't really matter too much in practice.
	r, err := s.selectRandomRecentPollination.Query(freshTime.Unix(), limit)
	if err != nil {
		return nil, err
	}
	var pollination STHPollination
	for r.Next() {
		var entry STHPollinationEntry
		if err := r.Scan(&entry.STHVersion, &entry.TreeSize, &entry.Timestamp, &entry.Sha256RootHashB64, &entry.TreeHeadSignatureB64, &entry.LogID); err != nil {
			return nil, err
		}
		pollination.STHs = append(pollination.STHs, entry)
	}
	// If there are no entries to return, wedge an empty array in there so that the json encoder returns something valid.
	if pollination.STHs == nil {
		pollination.STHs = make([]STHPollinationEntry, 0)
	}
	return &pollination, nil
}

// AddSTHPollination stores the passed in pollination object.
func (s *Storage) AddSTHPollination(pollination STHPollination) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// If we return a non-nil error, then rollback the transaction.
	defer func() {
		if err != nil {
			tx.Rollback()
		}
		err = tx.Commit()
	}()

	for _, pe := range pollination.STHs {
		if err := s.addSTHPollinationEntryIfNotExists(tx, pe); err != nil {
			return err
		}
	}
	return nil
}

func (s *Storage) getSCTID(sct string) (int64, error) {
	return selectThingID(s.selectSCTID, sct)
}

func (s *Storage) getChainID(chain []string) (int64, error) {
	flatChain := strings.Join(chain, "")
	return selectThingID(s.selectChainID, flatChain)
}

func getNumThings(getCount *sql.Stmt) (int64, error) {
	r, err := getCount.Query()
	if err != nil {
		return -1, err
	}
	if !r.Next() {
		return -1, fmt.Errorf("Empty scan returned while querying %v", getCount)
	}
	var count int64
	if err := r.Scan(&count); err != nil {
		return -1, err
	}
	return count, nil
}

func (s *Storage) getNumChains() (int64, error) {
	return getNumThings(s.selectNumChains)
}

func (s *Storage) getNumFeedback() (int64, error) {
	return getNumThings(s.selectNumFeedback)
}

func (s *Storage) getNumSCTs() (int64, error) {
	return getNumThings(s.selectNumSCTs)
}

func (s *Storage) getNumSTHs() (int64, error) {
	return getNumThings(s.selectNumSTHs)
}

func (s *Storage) hasFeedback(sctID, chainID int64) bool {
	r, err := s.selectFeedback.Query(sctID, chainID)
	if err != nil {
		return false
	}
	return r.Next()
}

func (s *Storage) hasSTH(version STHVersion, treeSize, timestamp int64, rootHash, signature, logID string) bool {
	r, err := s.selectSTH.Query(version, treeSize, timestamp, rootHash, signature, logID)
	if err != nil {
		return false
	}
	return r.Next()
}