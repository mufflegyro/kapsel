CREATE VIRTUAL TABLE search_documents_fts USING fts5(
  owner_type UNINDEXED,
  owner_id UNINDEXED,
  field UNINDEXED,
  text,
  content = 'search_documents',
  content_rowid = 'rowid',
  tokenize = 'unicode61'
);

CREATE TRIGGER search_documents_ai AFTER INSERT ON search_documents BEGIN
  INSERT INTO search_documents_fts(rowid, owner_type, owner_id, field, text)
  VALUES (new.rowid, new.owner_type, new.owner_id, new.field, new.text);
END;

CREATE TRIGGER search_documents_ad AFTER DELETE ON search_documents BEGIN
  INSERT INTO search_documents_fts(search_documents_fts, rowid, owner_type, owner_id, field, text)
  VALUES ('delete', old.rowid, old.owner_type, old.owner_id, old.field, old.text);
END;

CREATE TRIGGER search_documents_au AFTER UPDATE ON search_documents BEGIN
  INSERT INTO search_documents_fts(search_documents_fts, rowid, owner_type, owner_id, field, text)
  VALUES ('delete', old.rowid, old.owner_type, old.owner_id, old.field, old.text);

  INSERT INTO search_documents_fts(rowid, owner_type, owner_id, field, text)
  VALUES (new.rowid, new.owner_type, new.owner_id, new.field, new.text);
END;

INSERT INTO search_documents_fts(search_documents_fts) VALUES ('rebuild');
