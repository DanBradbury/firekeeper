-- Synthetic OpenCode database in the layout before session_v2: sessions in
-- session, messages in message, and message content in part.
CREATE TABLE `session` (
	`id` text PRIMARY KEY,
	`project_id` text NOT NULL,
	`parent_id` text,
	`slug` text NOT NULL,
	`directory` text NOT NULL,
	`title` text NOT NULL,
	`version` text NOT NULL,
	`time_created` integer NOT NULL,
	`time_updated` integer NOT NULL,
	`model` text
);
CREATE TABLE `message` (
	`id` text PRIMARY KEY,
	`session_id` text NOT NULL,
	`time_created` integer NOT NULL,
	`time_updated` integer NOT NULL,
	`data` text NOT NULL
);
CREATE TABLE `part` (
	`id` text PRIMARY KEY,
	`message_id` text NOT NULL,
	`session_id` text NOT NULL,
	`time_created` integer NOT NULL,
	`time_updated` integer NOT NULL,
	`data` text NOT NULL
);

INSERT INTO session VALUES ('ses_legacyA', 'prj', NULL, 's', '/work/demo', 'Legacy demo', '1',
	1791892800000, 1791892900000, '{"id":"model-test","providerID":"provider-test"}');
INSERT INTO session VALUES ('ses_legacyNew', 'prj', NULL, 's', '/work/other', 'New session - 2026-10-13T12:00:00.000Z', '1',
	1791892800000, 1791892800000, NULL);
INSERT INTO session VALUES ('ses_legacyChild', 'prj', 'ses_legacyA', 's', '/work/demo', 'Child', '1',
	1791892800000, 1791892900000, NULL);

INSERT INTO message VALUES ('msg_1', 'ses_legacyA', 1791892801000, 1791892801000,
	'{"role":"user","time":{"created":1791892801000},"model":{"providerID":"provider-test","modelID":"model-test"},"summary":{"diffs":["large"]}}');
INSERT INTO part VALUES ('prt_1', 'msg_1', 'ses_legacyA', 1791892801000, 1791892801000,
	'{"type":"text","text":"fix the bug"}');

INSERT INTO message VALUES ('msg_2', 'ses_legacyA', 1791892802000, 1791892803000,
	'{"role":"assistant","time":{"created":1791892802000,"completed":1791892803000},"modelID":"model-test","providerID":"provider-test","tokens":{"input":10,"output":4,"reasoning":1,"cache":{"read":100,"write":5}}}');
INSERT INTO part VALUES ('prt_2', 'msg_2', 'ses_legacyA', 1791892802000, 1791892802000,
	'{"type":"step-start"}');
INSERT INTO part VALUES ('prt_3', 'msg_2', 'ses_legacyA', 1791892802000, 1791892802000,
	'{"type":"tool","tool":"edit","callID":"c1","state":{"status":"completed","input":{"filePath":"/work/demo/main.go"},"output":"edited"}}');
INSERT INTO part VALUES ('prt_4', 'msg_2', 'ses_legacyA', 1791892803000, 1791892803000,
	'{"type":"text","text":"done"}');

INSERT INTO message VALUES ('msg_3', 'ses_legacyA', 1791892804000, 1791892804000,
	'{"role":"assistant","time":{"created":1791892804000},"modelID":"model-test"}');
INSERT INTO part VALUES ('prt_4b', 'msg_3', 'ses_legacyA', 1791892804000, 1791892804000, 'not json');
INSERT INTO part VALUES ('prt_5', 'msg_3', 'ses_legacyA', 1791892804000, 1791892804000,
	'{"type":"tool","tool":"bash","callID":"c2","state":{"status":"error","input":{"command":"false"},"error":"exit 1"}}');

INSERT INTO message VALUES ('msg_n1', 'ses_legacyNew', 1791892801000, 1791892801000, '{"role":"user"}');
INSERT INTO part VALUES ('prt_n1', 'msg_n1', 'ses_legacyNew', 1791892801000, 1791892801000,
	'{"type":"text","text":"add a --verbose flag\nto the CLI"}');
