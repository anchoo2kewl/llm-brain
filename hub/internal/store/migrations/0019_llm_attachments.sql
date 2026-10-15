-- Files attached to a plain-chat message: images go to the model as images, text files as text. The bytes
-- are hub assets (0017).
CREATE TABLE IF NOT EXISTS llm_attachments (
  message_id INTEGER NOT NULL,
  asset_id   INTEGER NOT NULL,
  PRIMARY KEY (message_id, asset_id)
);
