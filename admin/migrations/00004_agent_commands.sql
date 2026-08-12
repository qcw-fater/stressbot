-- +goose Up
CREATE TABLE IF NOT EXISTS agent_commands (
  command_id VARCHAR(64) NOT NULL,
  sequence BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  agent_id VARCHAR(128) NOT NULL,
  task_id VARCHAR(64) NULL,
  kind VARCHAR(32) NOT NULL,
  payload MEDIUMBLOB NOT NULL,
  state VARCHAR(16) NOT NULL DEFAULT 'pending',
  created_at_unix_nano BIGINT NOT NULL,
  acknowledged_at_unix_nano BIGINT NULL,
  rejection_reason VARCHAR(1024) NULL,
  PRIMARY KEY (command_id),
  UNIQUE KEY uq_agent_commands_sequence (sequence),
  KEY idx_agent_commands_replay (agent_id, state, sequence),
  KEY idx_agent_commands_task_state (task_id, state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- +goose Down
DROP TABLE IF EXISTS agent_commands;
