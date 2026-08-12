package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"stressbot/controlplane/controlv1"

	"google.golang.org/protobuf/proto"
)

type MySQLCommandStore struct {
	db *sql.DB
}

func NewMySQLCommandStore(db *sql.DB) *MySQLCommandStore { return &MySQLCommandStore{db: db} }

func (s *MySQLCommandStore) CreateBatch(ctx context.Context, commands []*controlv1.Command) error {
	if len(commands) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO agent_commands
		(command_id, agent_id, task_id, kind, payload, state, created_at_unix_nano)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, 'pending', ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, command := range commands {
		if command == nil || command.CommandId == "" || command.AgentId == "" {
			return fmt.Errorf("命令身份字段无效")
		}
		kind, err := commandKind(command)
		if err != nil {
			return err
		}
		copyCommand := proto.Clone(command).(*controlv1.Command)
		copyCommand.Sequence = 0
		if copyCommand.CreatedAtUnixNano == 0 {
			copyCommand.CreatedAtUnixNano = time.Now().UnixNano()
		}
		payload, err := proto.Marshal(copyCommand)
		if err != nil {
			return err
		}
		result, err := stmt.ExecContext(ctx, copyCommand.CommandId, copyCommand.AgentId, copyCommand.TaskId, kind, payload, copyCommand.CreatedAtUnixNano)
		if err != nil {
			return err
		}
		sequence, err := result.LastInsertId()
		if err != nil || sequence <= 0 {
			return fmt.Errorf("读取命令序列失败: %w", err)
		}
		command.Sequence = uint64(sequence)
		command.CreatedAtUnixNano = copyCommand.CreatedAtUnixNano
	}
	return tx.Commit()
}

func (s *MySQLCommandStore) Get(ctx context.Context, id string) (*controlv1.Command, error) {
	row := s.db.QueryRowContext(ctx, `SELECT sequence, payload FROM agent_commands WHERE command_id = ?`, id)
	return scanCommand(row)
}

func (s *MySQLCommandStore) Pending(ctx context.Context, agentID string, after uint64, limit int) ([]*controlv1.Command, error) {
	if limit <= 0 || limit > commandReplayBatchSize {
		limit = commandReplayBatchSize
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, payload FROM agent_commands
		WHERE agent_id = ? AND state = 'pending' AND sequence > ?
		ORDER BY sequence LIMIT ?`, agentID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*controlv1.Command, 0, limit)
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, command)
	}
	return out, rows.Err()
}

type commandScanner interface {
	Scan(...any) error
}

func scanCommand(scanner commandScanner) (*controlv1.Command, error) {
	var sequence uint64
	var payload []byte
	if err := scanner.Scan(&sequence, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCommandNotFound
		}
		return nil, err
	}
	command := new(controlv1.Command)
	if err := proto.Unmarshal(payload, command); err != nil {
		return nil, fmt.Errorf("解析命令载荷失败: %w", err)
	}
	command.Sequence = sequence
	return command, nil
}

func (s *MySQLCommandStore) Acknowledge(ctx context.Context, id string, status controlv1.CommandAckStatus, reason string) error {
	state, err := commandState(status)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_commands
		SET state = ?, acknowledged_at_unix_nano = ?, rejection_reason = NULLIF(?, '')
		WHERE command_id = ? AND state = 'pending'`, state, time.Now().UnixNano(), reason, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		var exists bool
		if queryErr := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_commands WHERE command_id = ?)`, id).Scan(&exists); queryErr != nil {
			return queryErr
		}
		if !exists {
			return ErrCommandNotFound
		}
	}
	return nil
}

func (s *MySQLCommandStore) CancelPendingTaskCommands(ctx context.Context, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE agent_commands
		SET state = 'rejected', acknowledged_at_unix_nano = ?, rejection_reason = ?
		WHERE state = 'pending' AND task_id IS NOT NULL`, time.Now().UnixNano(), reason)
	return err
}

func (s *MySQLCommandStore) Close() error { return nil }
