package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	currentSchemaVersion           = 9
	migrationChecksumSchemaVersion = 4
)

type schemaMigration struct {
	version    int
	name       string
	statements []string
}

type migrationBackup struct {
	path        string
	size        int64
	checksum    string
	createdAt   int64
	fromVersion int
	toVersion   int
}

var schemaMigrations = []schemaMigration{
	{
		version: 1,
		name:    "job-runtime",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS schema_migrations (
				version INTEGER PRIMARY KEY,
				applied_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_status_created_at ON jobs(status, created_at, id)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_updated_at ON jobs(updated_at DESC)`,
			`CREATE TABLE IF NOT EXISTS job_runtime (
				job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
				stage TEXT NOT NULL DEFAULT 'queued',
				message TEXT NOT NULL DEFAULT '',
				progress REAL NOT NULL DEFAULT 0 CHECK(progress >= 0 AND progress <= 100),
				total_tasks INTEGER NOT NULL DEFAULT 0,
				completed_tasks INTEGER NOT NULL DEFAULT 0,
				failed_tasks INTEGER NOT NULL DEFAULT 0,
				raw_records INTEGER NOT NULL DEFAULT 0,
				unique_records INTEGER NOT NULL DEFAULT 0,
				duplicate_records INTEGER NOT NULL DEFAULT 0,
				websites_found INTEGER NOT NULL DEFAULT 0,
				emails_found INTEGER NOT NULL DEFAULT 0,
				warnings INTEGER NOT NULL DEFAULT 0,
				errors INTEGER NOT NULL DEFAULT 0,
				started_at INTEGER,
				finished_at INTEGER,
				last_checkpoint_at INTEGER,
				last_heartbeat_at INTEGER,
				last_error TEXT NOT NULL DEFAULT '',
				scraper_version TEXT NOT NULL DEFAULT '',
				config_snapshot TEXT NOT NULL DEFAULT '{}',
				archived INTEGER NOT NULL DEFAULT 0,
				folder TEXT NOT NULL DEFAULT '',
				notes TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS job_tasks (
				id TEXT PRIMARY KEY,
				job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				kind TEXT NOT NULL,
				state TEXT NOT NULL,
				sequence INTEGER NOT NULL DEFAULT 0,
				query TEXT NOT NULL DEFAULT '',
				source_cell TEXT NOT NULL DEFAULT '',
				payload TEXT NOT NULL DEFAULT '{}',
				attempts INTEGER NOT NULL DEFAULT 0,
				max_attempts INTEGER NOT NULL DEFAULT 3,
				last_error TEXT NOT NULL DEFAULT '',
				started_at INTEGER,
				finished_at INTEGER,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_tasks_queue ON job_tasks(job_id, state, sequence, created_at)`,
			`CREATE TABLE IF NOT EXISTS job_checkpoints (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				stage TEXT NOT NULL,
				payload TEXT NOT NULL DEFAULT '{}',
				created_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_checkpoints_latest ON job_checkpoints(job_id, created_at DESC, id DESC)`,
			`CREATE TABLE IF NOT EXISTS job_logs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				level TEXT NOT NULL,
				stage TEXT NOT NULL DEFAULT '',
				message TEXT NOT NULL,
				context TEXT NOT NULL DEFAULT '{}',
				created_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_logs_job_created ON job_logs(job_id, created_at DESC, id DESC)`,
			`INSERT OR IGNORE INTO job_runtime (
				job_id, stage, progress, config_snapshot, created_at, updated_at
			)
			SELECT id,
				CASE status
					WHEN 'pending' THEN 'queued'
					WHEN 'working' THEN 'recovering'
					WHEN 'ok' THEN 'completed'
					WHEN 'failed' THEN 'failed'
					ELSE status
				END,
				CASE WHEN status = 'ok' THEN 100 ELSE 0 END,
				data,
				created_at,
				updated_at
			FROM jobs`,
		},
	},
	{
		version: 2,
		name:    "business-results",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS businesses (
				id TEXT PRIMARY KEY,
				canonical_key TEXT NOT NULL UNIQUE,
				place_id TEXT NOT NULL DEFAULT '',
				cid TEXT NOT NULL DEFAULT '',
				data_id TEXT NOT NULL DEFAULT '',
				input_id TEXT NOT NULL DEFAULT '',
				maps_url TEXT NOT NULL DEFAULT '',
				name TEXT NOT NULL,
				normalized_name TEXT NOT NULL,
				primary_category TEXT NOT NULL DEFAULT '',
				categories TEXT NOT NULL DEFAULT '[]',
				description TEXT NOT NULL DEFAULT '',
				business_status TEXT NOT NULL DEFAULT '',
				claimed INTEGER,
				address TEXT NOT NULL DEFAULT '',
				normalized_address TEXT NOT NULL DEFAULT '',
				street TEXT NOT NULL DEFAULT '',
				city TEXT NOT NULL DEFAULT '',
				state TEXT NOT NULL DEFAULT '',
				postal_code TEXT NOT NULL DEFAULT '',
				country TEXT NOT NULL DEFAULT '',
				latitude REAL,
				longitude REAL,
				plus_code TEXT NOT NULL DEFAULT '',
				phone TEXT NOT NULL DEFAULT '',
				normalized_phone TEXT NOT NULL DEFAULT '',
				website TEXT NOT NULL DEFAULT '',
				domain TEXT NOT NULL DEFAULT '',
				emails TEXT NOT NULL DEFAULT '',
				rating REAL,
				review_count INTEGER,
				reviews_per_rating TEXT NOT NULL DEFAULT '{}',
				open_hours TEXT NOT NULL DEFAULT '{}',
				popular_times TEXT NOT NULL DEFAULT '{}',
				price_range TEXT NOT NULL DEFAULT '',
				social_profiles TEXT NOT NULL DEFAULT '{}',
				quality_score REAL NOT NULL DEFAULT 0,
				quality_confidence REAL NOT NULL DEFAULT 0,
				reviewed INTEGER NOT NULL DEFAULT 0,
				notes TEXT NOT NULL DEFAULT '',
				raw_json TEXT NOT NULL DEFAULT '{}',
				first_seen_at INTEGER NOT NULL,
				last_seen_at INTEGER NOT NULL,
				last_changed_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_place_id ON businesses(place_id) WHERE place_id <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_cid ON businesses(cid) WHERE cid <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_domain ON businesses(domain) WHERE domain <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_phone ON businesses(normalized_phone) WHERE normalized_phone <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_location ON businesses(country, state, city, postal_code)`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_rating ON businesses(rating DESC, review_count DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_last_seen ON businesses(last_seen_at DESC)`,
			`CREATE VIRTUAL TABLE IF NOT EXISTS businesses_fts USING fts5(
				business_id UNINDEXED,
				name,
				categories,
				address,
				city,
				state,
				postal_code,
				country,
				emails,
				domain,
				notes,
				tokenize = 'unicode61 remove_diacritics 2'
			)`,
			`CREATE TABLE IF NOT EXISTS business_versions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
				content_hash TEXT NOT NULL,
				change_type TEXT NOT NULL,
				snapshot TEXT NOT NULL,
				observed_at INTEGER NOT NULL,
				UNIQUE(business_id, content_hash)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_business_versions_time ON business_versions(business_id, observed_at DESC)`,
			`CREATE TABLE IF NOT EXISTS business_sources (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
				source_type TEXT NOT NULL,
				source_url TEXT NOT NULL DEFAULT '',
				source_query TEXT NOT NULL DEFAULT '',
				source_cell TEXT NOT NULL DEFAULT '',
				input_id TEXT NOT NULL DEFAULT '',
				extraction_method TEXT NOT NULL DEFAULT '',
				confidence REAL NOT NULL DEFAULT 1,
				extracted_at INTEGER NOT NULL,
				UNIQUE(business_id, job_id, source_type, source_url, source_query, source_cell)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_business_sources_job ON business_sources(job_id, business_id)`,
			`CREATE TABLE IF NOT EXISTS field_provenance (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				field_name TEXT NOT NULL,
				original_value TEXT NOT NULL DEFAULT '',
				normalized_value TEXT NOT NULL DEFAULT '',
				preferred INTEGER NOT NULL DEFAULT 0,
				source_type TEXT NOT NULL,
				source_url TEXT NOT NULL DEFAULT '',
				source_query TEXT NOT NULL DEFAULT '',
				source_cell TEXT NOT NULL DEFAULT '',
				extraction_method TEXT NOT NULL DEFAULT '',
				confidence REAL NOT NULL DEFAULT 1,
				extracted_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_field_provenance_business_field ON field_provenance(business_id, field_name, preferred DESC, extracted_at DESC)`,
			`CREATE TABLE IF NOT EXISTS websites (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				url TEXT NOT NULL,
				final_url TEXT NOT NULL DEFAULT '',
				domain TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT 'unknown',
				http_status INTEGER,
				https INTEGER,
				response_time_ms INTEGER,
				redirect_chain TEXT NOT NULL DEFAULT '[]',
				page_title TEXT NOT NULL DEFAULT '',
				meta_description TEXT NOT NULL DEFAULT '',
				language TEXT NOT NULL DEFAULT '',
				technologies TEXT NOT NULL DEFAULT '[]',
				social_links TEXT NOT NULL DEFAULT '{}',
				screenshot_path TEXT NOT NULL DEFAULT '',
				last_checked_at INTEGER,
				UNIQUE(business_id, url)
			)`,
			`CREATE TABLE IF NOT EXISTS emails (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				value TEXT NOT NULL,
				normalized_value TEXT NOT NULL,
				kind TEXT NOT NULL DEFAULT 'unknown',
				status TEXT NOT NULL DEFAULT 'unverified',
				domain_has_mx INTEGER,
				disposable INTEGER NOT NULL DEFAULT 0,
				source_url TEXT NOT NULL DEFAULT '',
				extraction_method TEXT NOT NULL DEFAULT '',
				confidence REAL NOT NULL DEFAULT 0,
				last_checked_at INTEGER,
				UNIQUE(business_id, normalized_value)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_emails_value ON emails(normalized_value)`,
			`CREATE TABLE IF NOT EXISTS phones (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				value TEXT NOT NULL,
				normalized_value TEXT NOT NULL,
				kind TEXT NOT NULL DEFAULT 'unknown',
				source_url TEXT NOT NULL DEFAULT '',
				confidence REAL NOT NULL DEFAULT 0,
				UNIQUE(business_id, normalized_value)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_phones_value ON phones(normalized_value)`,
			`CREATE TABLE IF NOT EXISTS social_profiles (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				platform TEXT NOT NULL,
				url TEXT NOT NULL,
				source_url TEXT NOT NULL DEFAULT '',
				confidence REAL NOT NULL DEFAULT 0,
				UNIQUE(business_id, platform, url)
			)`,
		},
	},
	{
		version: 3,
		name:    "local-workspace",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS tags (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE COLLATE NOCASE,
				color TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS business_tags (
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
				PRIMARY KEY(business_id, tag_id)
			)`,
			`CREATE TABLE IF NOT EXISTS job_tags (
				job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
				PRIMARY KEY(job_id, tag_id)
			)`,
			`CREATE TABLE IF NOT EXISTS notes (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				entity_type TEXT NOT NULL,
				entity_id TEXT NOT NULL,
				body TEXT NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_notes_entity ON notes(entity_type, entity_id, updated_at DESC)`,
			`CREATE TABLE IF NOT EXISTS saved_views (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				entity_type TEXT NOT NULL DEFAULT 'businesses',
				filters TEXT NOT NULL DEFAULT '{}',
				columns TEXT NOT NULL DEFAULT '[]',
				sort TEXT NOT NULL DEFAULT '[]',
				grouping TEXT NOT NULL DEFAULT '[]',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS templates (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				configuration TEXT NOT NULL,
				tags TEXT NOT NULL DEFAULT '[]',
				folder TEXT NOT NULL DEFAULT '',
				pinned INTEGER NOT NULL DEFAULT 0,
				use_count INTEGER NOT NULL DEFAULT 0,
				last_run_at INTEGER,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS saved_areas (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				geojson TEXT NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS schedules (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				template_id TEXT REFERENCES templates(id) ON DELETE SET NULL,
				cron_expression TEXT NOT NULL,
				timezone TEXT NOT NULL DEFAULT 'UTC',
				enabled INTEGER NOT NULL DEFAULT 1,
				overlap_policy TEXT NOT NULL DEFAULT 'queue',
				missed_run_policy TEXT NOT NULL DEFAULT 'skip',
				configuration TEXT NOT NULL DEFAULT '{}',
				next_run_at INTEGER,
				last_run_at INTEGER,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS schedule_runs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				schedule_id TEXT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
				job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
				state TEXT NOT NULL,
				scheduled_for INTEGER NOT NULL,
				started_at INTEGER,
				finished_at INTEGER,
				error TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE IF NOT EXISTS proxy_pools (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL UNIQUE COLLATE NOCASE,
				strategy TEXT NOT NULL DEFAULT 'round_robin',
				settings TEXT NOT NULL DEFAULT '{}',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS proxies (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL DEFAULT '',
				url_encrypted TEXT NOT NULL,
				url_masked TEXT NOT NULL,
				protocol TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1,
				status TEXT NOT NULL DEFAULT 'unknown',
				exit_ip TEXT NOT NULL DEFAULT '',
				country TEXT NOT NULL DEFAULT '',
				latency_ms INTEGER,
				success_count INTEGER NOT NULL DEFAULT 0,
				failure_count INTEGER NOT NULL DEFAULT 0,
				block_count INTEGER NOT NULL DEFAULT 0,
				usage_count INTEGER NOT NULL DEFAULT 0,
				last_success_at INTEGER,
				last_failure_at INTEGER,
				cooldown_until INTEGER,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS proxy_pool_members (
				pool_id TEXT NOT NULL REFERENCES proxy_pools(id) ON DELETE CASCADE,
				proxy_id TEXT NOT NULL REFERENCES proxies(id) ON DELETE CASCADE,
				PRIMARY KEY(pool_id, proxy_id)
			)`,
			`CREATE TABLE IF NOT EXISTS proxy_health (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				proxy_id TEXT NOT NULL REFERENCES proxies(id) ON DELETE CASCADE,
				status TEXT NOT NULL,
				latency_ms INTEGER,
				exit_ip TEXT NOT NULL DEFAULT '',
				country TEXT NOT NULL DEFAULT '',
				error TEXT NOT NULL DEFAULT '',
				checked_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_proxy_health_latest ON proxy_health(proxy_id, checked_at DESC)`,
			`CREATE TABLE IF NOT EXISTS exports (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				format TEXT NOT NULL,
				state TEXT NOT NULL,
				source_type TEXT NOT NULL,
				source_id TEXT NOT NULL DEFAULT '',
				filters TEXT NOT NULL DEFAULT '{}',
				columns TEXT NOT NULL DEFAULT '[]',
				file_path TEXT NOT NULL DEFAULT '',
				record_count INTEGER NOT NULL DEFAULT 0,
				file_size INTEGER NOT NULL DEFAULT 0,
				checksum TEXT NOT NULL DEFAULT '',
				error TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				finished_at INTEGER
			)`,
			`CREATE INDEX IF NOT EXISTS idx_exports_created ON exports(created_at DESC)`,
			`CREATE TABLE IF NOT EXISTS settings (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL,
				secret INTEGER NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS audit_logs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				action TEXT NOT NULL,
				entity_type TEXT NOT NULL DEFAULT '',
				entity_id TEXT NOT NULL DEFAULT '',
				details TEXT NOT NULL DEFAULT '{}',
				created_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs(created_at DESC)`,
			`CREATE TABLE IF NOT EXISTS api_keys (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				key_hash TEXT NOT NULL UNIQUE,
				permission TEXT NOT NULL DEFAULT 'read',
				enabled INTEGER NOT NULL DEFAULT 1,
				last_used_at INTEGER,
				created_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS api_request_logs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				method TEXT NOT NULL,
				path TEXT NOT NULL,
				status_code INTEGER NOT NULL,
				duration_ms INTEGER NOT NULL,
				api_key_id TEXT REFERENCES api_keys(id) ON DELETE SET NULL,
				created_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_api_request_logs_created ON api_request_logs(created_at DESC)`,
			`CREATE TABLE IF NOT EXISTS integrations (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				kind TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1,
				configuration TEXT NOT NULL DEFAULT '{}',
				secret_configuration TEXT NOT NULL DEFAULT '{}',
				last_run_at INTEGER,
				last_error TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
		},
	},
	{
		version: 4,
		name:    "durable-local-data",
		statements: []string{
			`ALTER TABLE job_runtime ADD COLUMN state TEXT NOT NULL DEFAULT 'queued'
				CHECK(state IN ('draft', 'queued', 'starting', 'running', 'paused', 'cancelling', 'completed', 'partial', 'failed', 'cancelled'))`,
			`ALTER TABLE job_runtime ADD COLUMN current_config_version INTEGER NOT NULL DEFAULT 1`,
			`UPDATE job_runtime
			SET state = CASE (SELECT status FROM jobs WHERE jobs.id = job_runtime.job_id)
				WHEN 'pending' THEN 'queued'
				WHEN 'working' THEN 'running'
				WHEN 'ok' THEN 'completed'
				WHEN 'failed' THEN 'failed'
				ELSE 'queued'
			END`,
			`UPDATE job_runtime
			SET stage = CASE stage
				WHEN 'completed' THEN 'saving_exporting'
				ELSE 'preparing_queries'
			END
			WHERE stage IN ('queued', 'recovering', 'completed', 'failed', 'pending', 'working', 'ok')`,
			`CREATE INDEX IF NOT EXISTS idx_job_runtime_state ON job_runtime(state, updated_at DESC, job_id)`,
			`CREATE TABLE IF NOT EXISTS job_config_versions (
				job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				version INTEGER NOT NULL CHECK(version > 0),
				configuration TEXT NOT NULL,
				scraper_version TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				PRIMARY KEY(job_id, version)
			)`,
			`INSERT OR IGNORE INTO job_config_versions(job_id, version, configuration, scraper_version, created_at)
			SELECT jobs.id, 1, jobs.data, job_runtime.scraper_version, jobs.created_at
			FROM jobs
			LEFT JOIN job_runtime ON job_runtime.job_id = jobs.id`,
			`ALTER TABLE job_tasks ADD COLUMN parent_task_id TEXT REFERENCES job_tasks(id) ON DELETE SET NULL`,
			`ALTER TABLE job_tasks ADD COLUMN task_key TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE job_tasks ADD COLUMN input_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE job_tasks ADD COLUMN checkpoint TEXT NOT NULL DEFAULT '{}'`,
			`UPDATE job_tasks SET task_key = id WHERE task_key = ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_job_tasks_key ON job_tasks(job_id, task_key)`,
			`CREATE INDEX IF NOT EXISTS idx_job_tasks_parent ON job_tasks(parent_task_id)`,
			`ALTER TABLE job_checkpoints ADD COLUMN task_id TEXT REFERENCES job_tasks(id) ON DELETE SET NULL`,
			`CREATE INDEX IF NOT EXISTS idx_job_checkpoints_task ON job_checkpoints(task_id, created_at DESC)`,
			`ALTER TABLE job_logs ADD COLUMN task_id TEXT REFERENCES job_tasks(id) ON DELETE SET NULL`,
			`ALTER TABLE job_logs ADD COLUMN business_id TEXT`,
			`ALTER TABLE job_logs ADD COLUMN event_type TEXT NOT NULL DEFAULT 'information'`,
			`ALTER TABLE businesses ADD COLUMN merged_into_id TEXT REFERENCES businesses(id) ON DELETE SET NULL`,
			`ALTER TABLE businesses ADD COLUMN deleted_at INTEGER`,
			`ALTER TABLE businesses ADD COLUMN change_status TEXT NOT NULL DEFAULT 'unchanged'`,
			`ALTER TABLE businesses ADD COLUMN website_status TEXT NOT NULL DEFAULT 'unknown'`,
			`ALTER TABLE businesses ADD COLUMN website_response_ms INTEGER`,
			`ALTER TABLE businesses ADD COLUMN scoring_rule_version TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_active_seen ON businesses(deleted_at, last_seen_at DESC, id)`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_geo ON businesses(latitude, longitude)`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_quality ON businesses(quality_score DESC, id)`,
			`CREATE INDEX IF NOT EXISTS idx_businesses_status ON businesses(business_status, id)`,
			`ALTER TABLE business_sources RENAME TO business_sources_v2`,
			`CREATE TABLE business_sources (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
				task_id TEXT REFERENCES job_tasks(id) ON DELETE SET NULL,
				source_type TEXT NOT NULL,
				source_url TEXT NOT NULL DEFAULT '',
				source_query TEXT NOT NULL DEFAULT '',
				source_cell TEXT NOT NULL DEFAULT '',
				input_id TEXT NOT NULL DEFAULT '',
				extraction_method TEXT NOT NULL DEFAULT '',
				confidence REAL NOT NULL DEFAULT 1 CHECK(confidence >= 0 AND confidence <= 1),
				extracted_at INTEGER NOT NULL,
				raw_json TEXT NOT NULL DEFAULT '{}',
				normalized_json TEXT NOT NULL DEFAULT '{}',
				record_hash TEXT NOT NULL DEFAULT '',
				ingest_key TEXT NOT NULL DEFAULT ''
			)`,
			`INSERT INTO business_sources(
				id, business_id, job_id, source_type, source_url, source_query, source_cell,
				input_id, extraction_method, confidence, extracted_at, ingest_key
			)
			SELECT id, business_id, job_id, source_type, source_url, source_query, source_cell,
				input_id, extraction_method, confidence, extracted_at, 'migrated-source:' || id
			FROM business_sources_v2`,
			`DROP TABLE business_sources_v2`,
			`CREATE UNIQUE INDEX idx_business_sources_ingest_key ON business_sources(ingest_key) WHERE ingest_key <> ''`,
			`CREATE INDEX idx_business_sources_job ON business_sources(job_id, extracted_at DESC, id)`,
			`CREATE INDEX idx_business_sources_business ON business_sources(business_id, extracted_at DESC, id)`,
			`CREATE INDEX idx_business_sources_task ON business_sources(task_id, id)`,
			`ALTER TABLE business_versions RENAME TO business_versions_v2`,
			`CREATE TABLE business_versions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				version_no INTEGER NOT NULL CHECK(version_no > 0),
				previous_version_id INTEGER REFERENCES business_versions(id) ON DELETE SET NULL,
				job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
				source_id INTEGER REFERENCES business_sources(id) ON DELETE SET NULL,
				content_hash TEXT NOT NULL,
				change_type TEXT NOT NULL,
				changed_fields TEXT NOT NULL DEFAULT '[]',
				snapshot TEXT NOT NULL,
				observed_at INTEGER NOT NULL,
				UNIQUE(business_id, version_no)
			)`,
			`INSERT INTO business_versions(
				id, business_id, version_no, previous_version_id, job_id, content_hash,
				change_type, snapshot, observed_at
			)
			SELECT id,
				business_id,
				ROW_NUMBER() OVER (PARTITION BY business_id ORDER BY observed_at, id),
				LAG(id) OVER (PARTITION BY business_id ORDER BY observed_at, id),
				job_id,
				content_hash,
				change_type,
				snapshot,
				observed_at
			FROM business_versions_v2`,
			`DROP TABLE business_versions_v2`,
			`CREATE INDEX idx_business_versions_time ON business_versions(business_id, version_no DESC)`,
			`CREATE TABLE IF NOT EXISTS business_changes (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				from_version_id INTEGER REFERENCES business_versions(id) ON DELETE SET NULL,
				to_version_id INTEGER REFERENCES business_versions(id) ON DELETE SET NULL,
				field_name TEXT NOT NULL,
				before_value TEXT NOT NULL DEFAULT 'null',
				after_value TEXT NOT NULL DEFAULT 'null',
				change_kind TEXT NOT NULL DEFAULT 'updated',
				detected_at INTEGER NOT NULL
			)`,
			`CREATE INDEX idx_business_changes_history ON business_changes(business_id, detected_at DESC, id DESC)`,
			`CREATE TABLE IF NOT EXISTS job_businesses (
				job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				first_source_id INTEGER REFERENCES business_sources(id) ON DELETE SET NULL,
				first_seen_at INTEGER NOT NULL,
				last_seen_at INTEGER NOT NULL,
				occurrence_count INTEGER NOT NULL DEFAULT 1,
				is_new INTEGER NOT NULL DEFAULT 0,
				is_changed INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY(job_id, business_id)
			)`,
			`CREATE INDEX idx_job_businesses_business ON job_businesses(business_id, job_id)`,
			`ALTER TABLE field_provenance ADD COLUMN source_id INTEGER REFERENCES business_sources(id) ON DELETE SET NULL`,
			`ALTER TABLE field_provenance ADD COLUMN original_json TEXT NOT NULL DEFAULT 'null'`,
			`ALTER TABLE field_provenance ADD COLUMN normalized_json TEXT NOT NULL DEFAULT 'null'`,
			`ALTER TABLE field_provenance ADD COLUMN value_hash TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE field_provenance ADD COLUMN superseded_at INTEGER`,
			`ALTER TABLE field_provenance ADD COLUMN operator TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE field_provenance ADD COLUMN edit_reason TEXT NOT NULL DEFAULT ''`,
			`UPDATE field_provenance
			SET preferred = 0
			WHERE preferred = 1
				AND id NOT IN (
					SELECT MAX(id) FROM field_provenance WHERE preferred = 1 GROUP BY business_id, field_name
				)`,
			`CREATE UNIQUE INDEX idx_field_provenance_preferred
			ON field_provenance(business_id, field_name)
			WHERE preferred = 1 AND superseded_at IS NULL`,
			`CREATE TABLE IF NOT EXISTS business_identity_keys (
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				key_type TEXT NOT NULL,
				key_value TEXT NOT NULL,
				source_id INTEGER REFERENCES business_sources(id) ON DELETE SET NULL,
				confidence REAL NOT NULL DEFAULT 1 CHECK(confidence >= 0 AND confidence <= 1),
				created_at INTEGER NOT NULL,
				PRIMARY KEY(business_id, key_type, key_value)
			)`,
			`CREATE INDEX idx_business_identity_lookup ON business_identity_keys(key_type, key_value, business_id)`,
			`CREATE TABLE IF NOT EXISTS duplicate_candidates (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				left_business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				right_business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				score REAL NOT NULL CHECK(score >= 0 AND score <= 1),
				signals TEXT NOT NULL DEFAULT '{}',
				state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending', 'merged', 'keep_both', 'ignored')),
				created_at INTEGER NOT NULL,
				resolved_at INTEGER,
				resolution_note TEXT NOT NULL DEFAULT '',
				CHECK(left_business_id < right_business_id),
				UNIQUE(left_business_id, right_business_id)
			)`,
			`CREATE INDEX idx_duplicate_candidates_review ON duplicate_candidates(state, score DESC, id)`,
			`CREATE TABLE IF NOT EXISTS dedup_rules (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				rule_type TEXT NOT NULL,
				left_key TEXT NOT NULL,
				right_key TEXT NOT NULL,
				action TEXT NOT NULL CHECK(action IN ('keep_separate', 'force_merge')),
				reason TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				UNIQUE(rule_type, left_key, right_key, action)
			)`,
			`CREATE TABLE IF NOT EXISTS business_merges (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				source_business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE RESTRICT,
				target_business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE RESTRICT,
				candidate_id INTEGER REFERENCES duplicate_candidates(id) ON DELETE SET NULL,
				source_snapshot TEXT NOT NULL,
				reason TEXT NOT NULL DEFAULT '',
				operator TEXT NOT NULL DEFAULT '',
				merged_at INTEGER NOT NULL,
				CHECK(source_business_id <> target_business_id)
			)`,
			`DROP TRIGGER IF EXISTS businesses_fts_insert`,
			`DROP TRIGGER IF EXISTS businesses_fts_update`,
			`DROP TRIGGER IF EXISTS businesses_fts_delete`,
			`DELETE FROM businesses_fts`,
			`INSERT INTO businesses_fts(business_id, name, categories, address, city, state, postal_code, country, emails, domain, notes)
			SELECT id, name, categories, address, city, state, postal_code, country, emails, domain, notes FROM businesses`,
			`CREATE TRIGGER businesses_fts_insert AFTER INSERT ON businesses BEGIN
				INSERT INTO businesses_fts(business_id, name, categories, address, city, state, postal_code, country, emails, domain, notes)
				VALUES (new.id, new.name, new.categories, new.address, new.city, new.state, new.postal_code, new.country, new.emails, new.domain, new.notes);
			END`,
			`CREATE TRIGGER businesses_fts_update AFTER UPDATE ON businesses BEGIN
				DELETE FROM businesses_fts WHERE business_id = old.id;
				INSERT INTO businesses_fts(business_id, name, categories, address, city, state, postal_code, country, emails, domain, notes)
				VALUES (new.id, new.name, new.categories, new.address, new.city, new.state, new.postal_code, new.country, new.emails, new.domain, new.notes);
			END`,
			`CREATE TRIGGER businesses_fts_delete AFTER DELETE ON businesses BEGIN
				DELETE FROM businesses_fts WHERE business_id = old.id;
			END`,
			`CREATE TABLE IF NOT EXISTS export_presets (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL UNIQUE COLLATE NOCASE,
				format TEXT NOT NULL,
				columns TEXT NOT NULL DEFAULT '[]',
				filters TEXT NOT NULL DEFAULT '{}',
				sort TEXT NOT NULL DEFAULT '[]',
				options TEXT NOT NULL DEFAULT '{}',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`ALTER TABLE exports ADD COLUMN preset_id TEXT REFERENCES export_presets(id) ON DELETE SET NULL`,
			`ALTER TABLE exports ADD COLUMN saved_view_id TEXT REFERENCES saved_views(id) ON DELETE SET NULL`,
			`ALTER TABLE exports ADD COLUMN relative_path TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE exports ADD COLUMN options TEXT NOT NULL DEFAULT '{}'`,
			`ALTER TABLE exports ADD COLUMN started_at INTEGER`,
			`CREATE INDEX idx_exports_state_created ON exports(state, created_at DESC, id)`,
			`CREATE TABLE IF NOT EXISTS export_parts (
				export_id TEXT NOT NULL REFERENCES exports(id) ON DELETE CASCADE,
				part_number INTEGER NOT NULL CHECK(part_number > 0),
				relative_path TEXT NOT NULL,
				record_count INTEGER NOT NULL DEFAULT 0,
				file_size INTEGER NOT NULL DEFAULT 0,
				checksum TEXT NOT NULL DEFAULT '',
				PRIMARY KEY(export_id, part_number)
			)`,
			`ALTER TABLE settings ADD COLUMN value_type TEXT NOT NULL DEFAULT 'json'`,
			`ALTER TABLE settings ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE settings ADD COLUMN updated_by TEXT NOT NULL DEFAULT 'system'`,
			`CREATE TABLE IF NOT EXISTS backups (
				id TEXT PRIMARY KEY,
				kind TEXT NOT NULL,
				state TEXT NOT NULL,
				relative_path TEXT NOT NULL UNIQUE,
				schema_version INTEGER NOT NULL,
				file_size INTEGER NOT NULL DEFAULT 0,
				checksum TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				finished_at INTEGER,
				error TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX idx_backups_created ON backups(created_at DESC, id)`,
			`CREATE TABLE IF NOT EXISTS legacy_imports (
				job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
				relative_path TEXT NOT NULL,
				file_size INTEGER NOT NULL DEFAULT 0,
				file_mtime INTEGER NOT NULL DEFAULT 0,
				file_checksum TEXT NOT NULL DEFAULT '',
				state TEXT NOT NULL DEFAULT 'pending',
				last_row INTEGER NOT NULL DEFAULT 0,
				row_count INTEGER NOT NULL DEFAULT 0,
				imported_count INTEGER NOT NULL DEFAULT 0,
				error TEXT NOT NULL DEFAULT '',
				started_at INTEGER,
				finished_at INTEGER,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS schema_migration_checksums (
				version INTEGER PRIMARY KEY,
				name TEXT NOT NULL,
				checksum TEXT NOT NULL,
				applied_at INTEGER NOT NULL
			)`,
		},
	},
	{
		version: 5,
		name:    "job-lifecycle-events",
		statements: []string{
			`ALTER TABLE job_runtime ADD COLUMN state_version INTEGER NOT NULL DEFAULT 0 CHECK(state_version >= 0)`,
			`ALTER TABLE job_runtime ADD COLUMN requested_stop TEXT NOT NULL DEFAULT ''
				CHECK(requested_stop IN ('', 'completed', 'pause_requested', 'user_cancelled', 'runtime_limit',
				'maximum_records', 'low_disk', 'shutdown', 'reconfigure', 'proxies_unavailable',
				'task_failures', 'tasks_incomplete', 'fatal_error'))`,
			`ALTER TABLE job_runtime ADD COLUMN outcome_reason TEXT NOT NULL DEFAULT ''
				CHECK(outcome_reason IN ('', 'completed', 'pause_requested', 'user_cancelled', 'runtime_limit',
				'maximum_records', 'low_disk', 'shutdown', 'reconfigure', 'proxies_unavailable',
				'task_failures', 'tasks_incomplete', 'fatal_error'))`,
			`ALTER TABLE job_runtime ADD COLUMN recovery_required INTEGER NOT NULL DEFAULT 0 CHECK(recovery_required IN (0, 1))`,
			`ALTER TABLE job_runtime ADD COLUMN queue_seq INTEGER NOT NULL DEFAULT 0 CHECK(queue_seq >= 0)`,
			`ALTER TABLE job_runtime ADD COLUMN queued_at INTEGER`,
			`ALTER TABLE job_runtime ADD COLUMN runtime_used_ms INTEGER NOT NULL DEFAULT 0 CHECK(runtime_used_ms >= 0)`,
			`ALTER TABLE job_runtime ADD COLUMN runtime_limit_ms INTEGER NOT NULL DEFAULT 0 CHECK(runtime_limit_ms >= 0)`,
			`ALTER TABLE job_runtime ADD COLUMN desired_concurrency INTEGER NOT NULL DEFAULT 0 CHECK(desired_concurrency >= 0)`,
			`ALTER TABLE job_runtime ADD COLUMN effective_concurrency INTEGER NOT NULL DEFAULT 0 CHECK(effective_concurrency >= 0)`,
			`ALTER TABLE job_runtime ADD COLUMN proxy_pool_id TEXT REFERENCES proxy_pools(id) ON DELETE SET NULL`,
			`UPDATE job_runtime
			SET state = 'paused',
				recovery_required = 1,
				message = CASE WHEN message = '' THEN 'Previous process stopped; resume from the last safe checkpoint.' ELSE message END,
				stage = CASE WHEN stage = '' THEN 'preparing_queries' ELSE stage END,
				updated_at = CAST(strftime('%s','now') AS INTEGER)
			WHERE state IN ('starting', 'running', 'cancelling')`,
			`UPDATE jobs SET status = 'pending', updated_at = CAST(strftime('%s','now') AS INTEGER)
			WHERE id IN (SELECT job_id FROM job_runtime WHERE recovery_required = 1)`,
			`WITH queued AS (
				SELECT job_runtime.job_id,
					ROW_NUMBER() OVER (ORDER BY jobs.created_at, jobs.id) AS sequence,
					jobs.created_at AS created_at
				FROM job_runtime
				JOIN jobs ON jobs.id = job_runtime.job_id
				WHERE job_runtime.state = 'queued'
			)
			UPDATE job_runtime
			SET queue_seq = COALESCE((SELECT sequence FROM queued WHERE queued.job_id = job_runtime.job_id), queue_seq),
				queued_at = COALESCE((SELECT created_at FROM queued WHERE queued.job_id = job_runtime.job_id), queued_at)
			WHERE state = 'queued'`,
			`CREATE INDEX idx_job_runtime_queue ON job_runtime(state, queue_seq, queued_at, job_id)`,
			`CREATE TABLE IF NOT EXISTS job_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				type TEXT NOT NULL,
				severity TEXT NOT NULL DEFAULT 'information',
				stage TEXT NOT NULL DEFAULT '',
				message TEXT NOT NULL,
				context TEXT NOT NULL DEFAULT '{}',
				created_at INTEGER NOT NULL
			)`,
			`CREATE INDEX idx_job_events_replay ON job_events(job_id, id)`,
			`CREATE INDEX idx_job_events_time ON job_events(job_id, created_at DESC, id DESC)`,
			`CREATE TABLE IF NOT EXISTS job_progress (
				job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
				stage TEXT NOT NULL DEFAULT 'preparing_queries',
				skipped_tasks INTEGER NOT NULL DEFAULT 0,
				active_tasks INTEGER NOT NULL DEFAULT 0,
				retries INTEGER NOT NULL DEFAULT 0,
				places_per_minute REAL NOT NULL DEFAULT 0,
				eta_seconds INTEGER,
				current_query TEXT NOT NULL DEFAULT '',
				current_cell TEXT NOT NULL DEFAULT '',
				current_domain TEXT NOT NULL DEFAULT '',
				browser_count INTEGER NOT NULL DEFAULT 0,
				active_pages INTEGER NOT NULL DEFAULT 0,
				cpu_percent REAL NOT NULL DEFAULT 0,
				memory_bytes INTEGER NOT NULL DEFAULT 0,
				disk_free_bytes INTEGER NOT NULL DEFAULT 0,
				database_writes INTEGER NOT NULL DEFAULT 0,
				website_queue INTEGER NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL
			)`,
			`INSERT OR IGNORE INTO job_progress(job_id, stage, updated_at)
			SELECT job_id, stage, updated_at FROM job_runtime`,
		},
	},
	{
		version: 6,
		name:    "explainable-quality-scoring",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS quality_rule_sets (
				version TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				rules TEXT NOT NULL,
				active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0, 1)),
				created_at INTEGER NOT NULL
			)`,
			`CREATE UNIQUE INDEX idx_quality_rule_sets_active
			ON quality_rule_sets(active) WHERE active = 1`,
			`CREATE TABLE IF NOT EXISTS business_score_components (
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				rule_version TEXT NOT NULL REFERENCES quality_rule_sets(version) ON DELETE RESTRICT,
				component TEXT NOT NULL,
				contribution REAL NOT NULL,
				maximum REAL NOT NULL CHECK(maximum >= 0),
				passed INTEGER NOT NULL DEFAULT 0 CHECK(passed IN (0, 1)),
				reason TEXT NOT NULL,
				evaluated_at INTEGER NOT NULL,
				PRIMARY KEY(business_id, rule_version, component)
			)`,
			`CREATE INDEX idx_business_score_components_business
			ON business_score_components(business_id, evaluated_at DESC, component)`,
		},
	},
	{
		version: 7,
		name:    "durable-website-enrichment",
		statements: []string{
			`ALTER TABLE websites ADD COLUMN tls_valid INTEGER`,
			`ALTER TABLE websites ADD COLUMN certificate_error TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE websites ADD COLUMN pages_checked INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN internal_links_checked INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN broken_internal_links INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN mixed_content INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN parked INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN coming_soon INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN placeholder INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN trackers TEXT NOT NULL DEFAULT '[]'`,
			`ALTER TABLE emails ADD COLUMN valid_syntax INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE emails ADD COLUMN role TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE emails ADD COLUMN personal_likely INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE emails ADD COLUMN mx_status TEXT NOT NULL DEFAULT 'not_checked'`,
			`ALTER TABLE emails ADD COLUMN mx_records TEXT NOT NULL DEFAULT '[]'`,
			`ALTER TABLE emails ADD COLUMN relevance INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE emails ADD COLUMN rank INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE website_audits (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				website_id INTEGER REFERENCES websites(id) ON DELETE SET NULL,
				task_id TEXT NOT NULL DEFAULT '',
				requested_url TEXT NOT NULL,
				final_url TEXT NOT NULL DEFAULT '',
				reachable INTEGER NOT NULL DEFAULT 0,
				status_code INTEGER NOT NULL DEFAULT 0,
				https INTEGER NOT NULL DEFAULT 0,
				tls_valid INTEGER NOT NULL DEFAULT 0,
				certificate_error TEXT NOT NULL DEFAULT '',
				response_time_ms INTEGER NOT NULL DEFAULT 0,
				redirect_chain TEXT NOT NULL DEFAULT '[]',
				internal_links_checked INTEGER NOT NULL DEFAULT 0,
				broken_internal_link_count INTEGER NOT NULL DEFAULT 0,
				broken_internal_links TEXT NOT NULL DEFAULT '[]',
				mixed_content INTEGER NOT NULL DEFAULT 0,
				parked INTEGER NOT NULL DEFAULT 0,
				coming_soon INTEGER NOT NULL DEFAULT 0,
				placeholder INTEGER NOT NULL DEFAULT 0,
				template_indicators TEXT NOT NULL DEFAULT '[]',
				technologies TEXT NOT NULL DEFAULT '[]',
				trackers TEXT NOT NULL DEFAULT '[]',
				emails TEXT NOT NULL DEFAULT '[]',
				phones TEXT NOT NULL DEFAULT '[]',
				social_profiles TEXT NOT NULL DEFAULT '[]',
				options TEXT NOT NULL DEFAULT '{}',
				raw_result TEXT NOT NULL DEFAULT '{}',
				error TEXT NOT NULL DEFAULT '',
				started_at INTEGER NOT NULL,
				completed_at INTEGER NOT NULL
			)`,
			`CREATE INDEX idx_website_audits_business_time
			ON website_audits(business_id, completed_at DESC, id DESC)`,
			`CREATE UNIQUE INDEX idx_website_audits_task ON website_audits(task_id) WHERE task_id <> ''`,
			`CREATE TABLE website_audit_pages (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				audit_id INTEGER NOT NULL REFERENCES website_audits(id) ON DELETE CASCADE,
				requested_url TEXT NOT NULL,
				final_url TEXT NOT NULL DEFAULT '',
				page_kind TEXT NOT NULL,
				status_code INTEGER NOT NULL DEFAULT 0,
				response_time_ms INTEGER NOT NULL DEFAULT 0,
				size_bytes INTEGER NOT NULL DEFAULT 0,
				body_truncated INTEGER NOT NULL DEFAULT 0,
				content_type TEXT NOT NULL DEFAULT '',
				page_title TEXT NOT NULL DEFAULT '',
				meta_description TEXT NOT NULL DEFAULT '',
				language TEXT NOT NULL DEFAULT '',
				mobile_viewport INTEGER NOT NULL DEFAULT 0,
				mixed_content INTEGER NOT NULL DEFAULT 0,
				copyright_year INTEGER NOT NULL DEFAULT 0,
				old_copyright INTEGER NOT NULL DEFAULT 0,
				redirects TEXT NOT NULL DEFAULT '[]',
				error TEXT NOT NULL DEFAULT '',
				UNIQUE(audit_id, requested_url, page_kind)
			)`,
			`CREATE INDEX idx_website_audit_pages_audit ON website_audit_pages(audit_id, id)`,
			`CREATE TABLE contact_evidence (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				audit_id INTEGER NOT NULL REFERENCES website_audits(id) ON DELETE CASCADE,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				contact_type TEXT NOT NULL,
				value TEXT NOT NULL,
				normalized_value TEXT NOT NULL DEFAULT '',
				source_url TEXT NOT NULL DEFAULT '',
				page_kind TEXT NOT NULL DEFAULT '',
				extraction_method TEXT NOT NULL DEFAULT '',
				confidence REAL NOT NULL DEFAULT 0,
				metadata TEXT NOT NULL DEFAULT '{}',
				created_at INTEGER NOT NULL,
				UNIQUE(audit_id, contact_type, normalized_value, source_url, extraction_method)
			)`,
			`CREATE INDEX idx_contact_evidence_business
			ON contact_evidence(business_id, contact_type, created_at DESC)`,
			`CREATE TABLE website_detections (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				audit_id INTEGER NOT NULL REFERENCES website_audits(id) ON DELETE CASCADE,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				detection_type TEXT NOT NULL CHECK(detection_type IN ('technology', 'tracker')),
				name TEXT NOT NULL,
				confidence REAL NOT NULL DEFAULT 0,
				evidence TEXT NOT NULL DEFAULT '[]',
				UNIQUE(audit_id, detection_type, name)
			)`,
			`CREATE INDEX idx_website_detections_business
			ON website_detections(business_id, detection_type, name)`,
			`CREATE TABLE enrichment_tasks (
				id TEXT PRIMARY KEY,
				business_id TEXT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
				job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
				website_url TEXT NOT NULL,
				state TEXT NOT NULL CHECK(state IN ('queued', 'running', 'completed', 'failed')),
				requested_by TEXT NOT NULL,
				options TEXT NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				audit_id INTEGER,
				error TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				started_at INTEGER,
				finished_at INTEGER,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE INDEX idx_enrichment_tasks_queue
			ON enrichment_tasks(state, created_at, id)`,
			`CREATE UNIQUE INDEX idx_enrichment_tasks_active_business
			ON enrichment_tasks(business_id) WHERE state IN ('queued', 'running')`,
		},
	},
	{
		version: 8,
		name:    "concurrent-task-leases",
		statements: []string{
			// A leased task lets several workers share one job's plan safely. The
			// lease owner is the only writer allowed to finish a task, and an
			// expired lease is reclaimed so a crashed worker cannot strand work.
			`ALTER TABLE job_tasks ADD COLUMN lease_owner TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE job_tasks ADD COLUMN lease_expires_at INTEGER`,
			`ALTER TABLE job_tasks ADD COLUMN heartbeat_at INTEGER`,
			`CREATE INDEX IF NOT EXISTS idx_job_tasks_lease
			ON job_tasks(state, lease_expires_at)`,
		},
	},
	{
		version: 9,
		name:    "automation-and-keyword-sets",
		statements: []string{
			// Schedule-level automation: bounded retries with backoff, an
			// optional export after a completed run, and retention for old runs.
			`ALTER TABLE schedules ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE schedules ADD COLUMN retry_backoff_seconds INTEGER NOT NULL DEFAULT 60`,
			`ALTER TABLE schedules ADD COLUMN auto_export_format TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE schedules ADD COLUMN runs_retention_days INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE schedule_runs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1`,
			// Reusable keyword sets for the wizard.
			`CREATE TABLE keyword_sets (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL UNIQUE COLLATE NOCASE,
				description TEXT NOT NULL DEFAULT '',
				keywords TEXT NOT NULL DEFAULT '[]',
				use_count INTEGER NOT NULL DEFAULT 0,
				last_used_at INTEGER,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE INDEX idx_keyword_sets_name ON keyword_sets(name)`,
		},
	},
}

func migrateDatabase(db *sql.DB, path string) error {
	version, err := supportedSchemaVersion(db)
	if err != nil {
		return err
	}

	if err := validateMigrationMetadata(db, version); err != nil {
		return err
	}
	if err := validateMigrationChecksums(db, version); err != nil {
		return err
	}

	var backup *migrationBackup

	if version < currentSchemaVersion {
		hasSchema, err := hasApplicationSchema(db)
		if err != nil {
			return err
		}

		if hasSchema {
			backup, err = backupBeforeMigration(db, path, version, currentSchemaVersion)
			if err != nil {
				return err
			}
		}
	}

	if err := ensureLegacySchema(db); err != nil {
		return err
	}

	for _, migration := range schemaMigrations {
		if migration.version <= version {
			continue
		}

		if err := applyMigration(db, migration); err != nil {
			return fmt.Errorf("apply database migration %d: %w", migration.version, err)
		}
	}

	version, err = supportedSchemaVersion(db)
	if err != nil {
		return err
	}
	if version != currentSchemaVersion {
		return fmt.Errorf("database migration ended at version %d, expected %d", version, currentSchemaVersion)
	}
	if err := validateMigrationMetadata(db, version); err != nil {
		return err
	}
	if err := validateMigrationChecksums(db, version); err != nil {
		return err
	}

	if backup != nil {
		if err := registerMigrationBackup(db, path, *backup); err != nil {
			return err
		}
	}

	return nil
}

func ensureLegacySchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		data TEXT NOT NULL,
		created_at INT NOT NULL,
		updated_at INT NOT NULL
	)`)

	return err
}

func schemaVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read database schema version: %w", err)
	}

	return version, nil
}

func supportedSchemaVersion(db *sql.DB) (int, error) {
	version, err := schemaVersion(db)
	if err != nil {
		return 0, err
	}

	if version > currentSchemaVersion {
		return 0, fmt.Errorf(
			"database schema version %d is newer than supported version %d",
			version,
			currentSchemaVersion,
		)
	}

	return version, nil
}

func applyMigration(db *sql.DB, migration schemaMigration) error {
	ctx := context.Background()

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}

	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	for _, statement := range migration.statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("execute statement: %w", err)
		}
	}

	// Version 4 introduces checksums. Backfill the complete applied history in
	// the same transaction so a crash cannot leave a current-version database
	// with missing checksum metadata that later has to be guessed or repaired.
	if migration.version >= migrationChecksumSchemaVersion {
		if err := recordMigrationChecksums(ctx, conn, migration.version); err != nil {
			return err
		}
	}

	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		migration.version,
		time.Now().UTC().Unix(),
	); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", migration.version)); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}

	committed = true

	return nil
}

func hasApplicationSchema(db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table'
			AND name NOT LIKE 'sqlite_%'`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect database schema: %w", err)
	}

	return count > 0, nil
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&count)
	if err != nil {
		return false, err
	}

	return count == 1, nil
}

func validateMigrationMetadata(db *sql.DB, version int) error {
	exists, err := tableExists(db, "schema_migrations")
	if err != nil {
		return fmt.Errorf("inspect migration metadata: %w", err)
	}

	if version == 0 {
		if !exists {
			return nil
		}

		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
			return fmt.Errorf("read migration metadata: %w", err)
		}

		if count != 0 {
			return fmt.Errorf("database migration metadata is inconsistent: user_version is 0 but %d migrations are recorded", count)
		}

		return nil
	}

	if !exists {
		return fmt.Errorf("database migration metadata is missing for schema version %d", version)
	}

	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read migration metadata: %w", err)
	}
	defer rows.Close()

	want := 1
	for rows.Next() {
		var applied int
		if err := rows.Scan(&applied); err != nil {
			return fmt.Errorf("scan migration metadata: %w", err)
		}

		if applied != want || applied > version {
			return fmt.Errorf("database migration metadata is inconsistent at version %d", applied)
		}

		want++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read migration metadata: %w", err)
	}

	if want-1 != version {
		return fmt.Errorf("database migration metadata ends at version %d, expected %d", want-1, version)
	}

	return nil
}

func migrationChecksum(migration schemaMigration) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d\x00%s\x00", migration.version, migration.name)

	for _, statement := range migration.statements {
		_, _ = io.WriteString(hash, statement)
		_, _ = io.WriteString(hash, "\x00")
	}

	return hex.EncodeToString(hash.Sum(nil))
}

func recordMigrationChecksums(ctx context.Context, executor sqlExecutor, throughVersion int) error {
	now := time.Now().UTC().Unix()
	for _, migration := range schemaMigrations {
		if migration.version > throughVersion {
			break
		}

		if _, err := executor.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO schema_migration_checksums(version, name, checksum, applied_at)
			VALUES (?, ?, ?, ?)`,
			migration.version,
			migration.name,
			migrationChecksum(migration),
			now,
		); err != nil {
			return fmt.Errorf("record migration checksum %d: %w", migration.version, err)
		}
	}

	return nil
}

func validateMigrationChecksums(db *sql.DB, version int) error {
	exists, err := tableExists(db, "schema_migration_checksums")
	if err != nil {
		return fmt.Errorf("inspect migration checksums: %w", err)
	}

	if version < migrationChecksumSchemaVersion {
		if exists {
			return fmt.Errorf(
				"database migration checksum metadata exists before schema version %d",
				migrationChecksumSchemaVersion,
			)
		}

		return nil
	}
	if !exists {
		return fmt.Errorf("database migration checksum metadata is missing for schema version %d", version)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migration_checksums`).Scan(&count); err != nil {
		return fmt.Errorf("count migration checksums: %w", err)
	}
	if count != version {
		return fmt.Errorf("database migration checksums contain %d entries, expected %d", count, version)
	}

	for _, migration := range schemaMigrations {
		if migration.version > version {
			break
		}

		var name, checksum string
		err := db.QueryRow(
			`SELECT name, checksum FROM schema_migration_checksums WHERE version = ?`,
			migration.version,
		).Scan(&name, &checksum)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("database migration checksum %d is missing", migration.version)
			}

			return fmt.Errorf("read migration checksum %d: %w", migration.version, err)
		}

		if name != migration.name || checksum != migrationChecksum(migration) {
			return fmt.Errorf("database migration checksum %d does not match this application", migration.version)
		}
	}

	return nil
}

func backupBeforeMigration(db *sql.DB, path string, fromVersion, toVersion int) (*migrationBackup, error) {
	if isMemoryDatabase(path) {
		return nil, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("inspect database before migration: %w", err)
	}

	if info.Size() == 0 {
		return nil, nil
	}

	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRow("PRAGMA wal_checkpoint(FULL)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return nil, fmt.Errorf("checkpoint database before migration: %w", err)
	}

	if busy != 0 {
		return nil, fmt.Errorf(
			"checkpoint database before migration: database is busy (%d of %d WAL frames checkpointed)",
			checkpointedFrames,
			logFrames,
		)
	}

	backupDir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return nil, fmt.Errorf("create migration backup directory: %w", err)
	}

	createdAt := time.Now().UTC()
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	backupName := fmt.Sprintf(
		"%s-schema-v%d-to-v%d-%s.db",
		name,
		fromVersion,
		toVersion,
		createdAt.Format("20060102T150405.000000000Z"),
	)
	backupPath := filepath.Join(backupDir, backupName)

	if _, err := db.Exec("VACUUM INTO ?", backupPath); err != nil {
		return nil, fmt.Errorf("create pre-migration backup: %w", err)
	}

	if err := verifySQLiteDatabase(backupPath); err != nil {
		return nil, fmt.Errorf("verify pre-migration backup: %w", err)
	}

	checksum, size, err := checksumFile(backupPath)
	if err != nil {
		return nil, fmt.Errorf("checksum pre-migration backup: %w", err)
	}

	backup := migrationBackup{
		path:        backupPath,
		size:        size,
		checksum:    checksum,
		createdAt:   createdAt.Unix(),
		fromVersion: fromVersion,
		toVersion:   toVersion,
	}

	manifestPath := backupPath + ".json"
	manifest, err := json.MarshalIndent(map[string]any{
		"kind":            "pre_migration",
		"database":        filepath.Base(backupPath),
		"from_version":    fromVersion,
		"to_version":      toVersion,
		"size_bytes":      size,
		"sha256":          checksum,
		"created_at_unix": createdAt.Unix(),
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode migration backup manifest: %w", err)
	}

	if err := os.WriteFile(manifestPath, append(manifest, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write migration backup manifest: %w", err)
	}

	return &backup, nil
}

func isMemoryDatabase(path string) bool {
	lowerPath := strings.ToLower(path)

	return path == "" || path == ":memory:" || strings.HasPrefix(lowerPath, "file::memory:") ||
		strings.Contains(lowerPath, "mode=memory")
}

func verifySQLiteDatabase(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()

	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}

	if result != "ok" {
		return fmt.Errorf("integrity check returned %q", result)
	}

	return nil
}

func checksumFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func registerMigrationBackup(db *sql.DB, databasePath string, backup migrationBackup) error {
	relativePath, err := filepath.Rel(filepath.Dir(databasePath), backup.path)
	if err != nil {
		return fmt.Errorf("resolve migration backup path: %w", err)
	}

	id := fmt.Sprintf("migration-%d-v%d-v%d", backup.createdAt, backup.fromVersion, backup.toVersion)
	_, err = db.Exec(
		`INSERT INTO backups(
			id, kind, state, relative_path, schema_version, file_size, checksum, created_at, finished_at
		) VALUES (?, 'pre_migration', 'completed', ?, ?, ?, ?, ?, ?)`,
		id,
		filepath.ToSlash(relativePath),
		backup.fromVersion,
		backup.size,
		backup.checksum,
		backup.createdAt,
		backup.createdAt,
	)
	if err != nil {
		return fmt.Errorf("register migration backup: %w", err)
	}

	return nil
}
