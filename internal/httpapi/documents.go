package httpapi

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/storage"
)

var allowedDocTypes = map[string]bool{
	"application/pdf": true,
	"image/png":       true, "image/jpeg": true, "image/webp": true,
	"text/plain": true, "text/csv": true,
}

const maxDocBytes = 10 << 20 // 10 MiB

type docView struct {
	ID        uuid.UUID `json:"id"`
	Filename  string    `json:"filename"`
	Content   string    `json:"contentType"`
	Size      int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
}

// store lazily builds the document backend (local disk default).
func (s *Server) store() (storage.Store, error) {
	return storage.NewLocal(storage.Dir())
}

// handleListDocuments GET /api/v1/shipments/{id}/documents
func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	user := currentUser(r)
	// Existence check first: foreign ids → 404 (no enumeration).
	if _, err := s.shipments.Get(r.Context(), user.TenantID, id); err != nil {
		s.domainError(w, err)
		return
	}
	out := []docView{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, filename, content_type, size_bytes, created_at
			FROM shipment_documents WHERE shipment_id=$1 ORDER BY created_at DESC`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v docView
			if err := rows.Scan(&v.ID, &v.Filename, &v.Content, &v.Size, &v.CreatedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUploadDocument POST /api/v1/shipments/{id}/documents (multipart)
func (s *Server) handleUploadDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	user := currentUser(r)
	ship, err := s.shipments.Get(r.Context(), user.TenantID, id)
	if err != nil {
		s.domainError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocBytes+1024)
	if err := r.ParseMultipartForm(maxDocBytes + 1024); err != nil {
		writeError(w, http.StatusBadRequest, "bad_upload", "File too large (max 10 MiB)")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_upload", "Multipart field 'file' required")
		return
	}
	defer f.Close()
	ctype := hdr.Header.Get("Content-Type")
	if !allowedDocTypes[strings.ToLower(strings.Split(ctype, ";")[0])] {
		writeError(w, http.StatusBadRequest, "bad_upload", "Allowed: PDF, PNG, JPEG, WEBP, TXT, CSV")
		return
	}
	st, err := s.store()
	if err != nil {
		s.domainError(w, err)
		return
	}
	key := storage.NewKey(user.TenantID, ship.ID, hdr.Filename)
	limited := io.LimitReader(f, maxDocBytes+1)
	if _, err := st.Put(key, limited); err != nil {
		s.domainError(w, err)
		return
	}
	var docID uuid.UUID
	var size int64
	err = db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		// Size comes from the stored object, so the declared byte count in the
		// multipart header is not trusted. The cast is required: without it
		// Postgres infers the bare $5 parameter as text inside the scalar
		// subquery and COALESCE cannot reconcile text with the integer 0.
		size, err = st.Size(key)
		if err != nil {
			return err
		}
		return tx.QueryRow(r.Context(), `
			INSERT INTO shipment_documents
				(tenant_id, shipment_id, filename, content_type, size_bytes, storage_key, uploaded_by)
			VALUES ($1,$2,$3,$4,$5::bigint,$6,$7)
			RETURNING id, size_bytes`,
			user.TenantID, ship.ID, hdr.Filename, strings.ToLower(strings.Split(ctype, ";")[0]),
			size, key, user.ID).Scan(&docID, &size)
	})
	if err != nil {
		_ = st.Delete(key)
		s.domainError(w, err)
		return
	}
	s.auditShipment(r, user, ship.ID, "document.upload", map[string]any{"filename": hdr.Filename})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": docID, "filename": hdr.Filename, "sizeBytes": size,
	})
}

// handleDownloadDocument GET /api/v1/documents/{id}/download
func (s *Server) handleDownloadDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid document id")
		return
	}
	user := currentUser(r)
	var filename, ctype, key string
	var size int64
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT filename, content_type, storage_key, size_bytes
			FROM shipment_documents WHERE id=$1`, id).Scan(&filename, &ctype, &key, &size)
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Document not found")
		return
	}
	st, err := s.store()
	if err != nil {
		s.domainError(w, err)
		return
	}
	rc, err := st.Open(key)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Document file missing")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// handleDeleteDocument DELETE /api/v1/documents/{id}
func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid document id")
		return
	}
	user := currentUser(r)
	var key string
	var shipmentID uuid.UUID
	var filename string
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `
			DELETE FROM shipment_documents WHERE id=$1
			RETURNING storage_key, shipment_id, filename`, id).Scan(&key, &shipmentID, &filename); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Document not found")
		return
	}
	if st, serr := s.store(); serr == nil {
		_ = st.Delete(key)
	}
	s.auditShipment(r, user, shipmentID, "document.delete", map[string]any{"filename": filename})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
