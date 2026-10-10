package imports

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

// Routes: every import step needs brand admin ("import" is in its job
// description, S9); starting the import also needs a passkey confirmation.
func (s *Service) Routes(mux *http.ServeMux, az stepUpper) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, f) }
	base := "/admin/v1/brands/{slug}/imports"
	mux.HandleFunc("POST "+base, h(s.upload))
	mux.HandleFunc("GET "+base, h(s.list))
	mux.HandleFunc("GET "+base+"/{id}", h(s.get))
	mux.HandleFunc("PUT "+base+"/{id}", h(s.setup))
	mux.HandleFunc("POST "+base+"/{id}/check", h(s.check))
	mux.HandleFunc("POST "+base+"/{id}/commit", h(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := az.RequireStepUp(r); err != nil {
			return err
		}
		return s.commit(w, r)
	}))
	mux.HandleFunc("POST "+base+"/{id}/cancel", h(s.cancel))
}

func (s *Service) brandAdmin(r *http.Request) (store.Brand, *auth.Principal, error) {
	return brands.WithRole(r, s.store, store.RoleBrandAdmin)
}

func (s *Service) loadImport(r *http.Request) (store.Brand, *auth.Principal, store.Import, error) {
	b, p, err := s.brandAdmin(r)
	if err != nil {
		return b, p, store.Import{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return b, p, store.Import{}, httpx.ErrNotFound
	}
	imp, err := s.store.ImportByID(r.Context(), b.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return b, p, imp, httpx.ErrNotFound
	}
	return b, p, imp, err
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) error {
	b, p, err := s.brandAdmin(r)
	if err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxFileBytes+1<<20)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return httpx.Errorf(http.StatusBadRequest, "bad_file", "attach a CSV file (max %d MB)", MaxFileBytes>>20)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return httpx.Errorf(http.StatusBadRequest, "bad_file", "the upload didn't finish: %v", err)
	}
	imp, err := s.Upload(r.Context(), b.ID, p.User.ID, filepath.Base(hdr.Filename), data)
	if err != nil {
		return err
	}
	return s.writeImport(w, r, http.StatusCreated, imp)
}

func (s *Service) writeImport(w http.ResponseWriter, r *http.Request, status int, imp store.Import) error {
	preview := imp.Preview
	if preview == nil {
		preview = [][]string{}
	}
	httpx.WriteJSON(w, status, map[string]any{
		"import": imp, "preview": preview, "attestation_text": AttestationText, "sources": sourceLabels,
	})
	return nil
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.brandAdmin(r)
	if err != nil {
		return err
	}
	list, err := s.store.ListImports(r.Context(), b.ID, 20)
	if err != nil {
		return err
	}
	if list == nil {
		list = []store.Import{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"imports": list})
	return nil
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	_, _, imp, err := s.loadImport(r)
	if err != nil {
		return err
	}
	return s.writeImport(w, r, http.StatusOK, imp)
}

func (s *Service) setup(w http.ResponseWriter, r *http.Request) error {
	b, p, imp, err := s.loadImport(r)
	if err != nil {
		return err
	}
	var in SetupInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := s.Setup(r.Context(), imp, p.User, in); err != nil {
		return err
	}
	if in.Consent.Attested {
		// Who signed the attestation, from where: part of the consent proof.
		if err := s.store.Audit(r.Context(), auth.AuditEntry(r, "import.attested", "import", imp.Filename, &b.ID), nil,
			map[string]any{"attestation": AttestationText, "source": in.Consent.Label(), "rows": imp.RowCount}); err != nil {
			return err
		}
	}
	imp, err = s.store.ImportByID(r.Context(), b.ID, imp.ID)
	if err != nil {
		return err
	}
	return s.writeImport(w, r, http.StatusOK, imp)
}

func (s *Service) check(w http.ResponseWriter, r *http.Request) error {
	b, _, imp, err := s.loadImport(r)
	if err != nil {
		return err
	}
	if err := s.RequestCheck(r.Context(), imp); err != nil {
		return err
	}
	return s.respondFresh(w, r, b.ID, imp.ID, http.StatusAccepted)
}

func (s *Service) commit(w http.ResponseWriter, r *http.Request) error {
	b, _, imp, err := s.loadImport(r)
	if err != nil {
		return err
	}
	if err := s.RequestCommit(r.Context(), imp); err != nil {
		return err
	}
	if err := s.store.Audit(r.Context(), auth.AuditEntry(r, "import.started", "import", imp.Filename, &b.ID), nil,
		map[string]any{"rows": imp.RowCount}); err != nil {
		return err
	}
	return s.respondFresh(w, r, b.ID, imp.ID, http.StatusAccepted)
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) error {
	b, _, imp, err := s.loadImport(r)
	if err != nil {
		return err
	}
	if err := s.store.CancelImport(r.Context(), imp.ID); errors.Is(err, store.ErrNotFound) {
		return httpx.Errorf(http.StatusConflict, "import_busy",
			"this import is finished, or still running (a run can be cancelled once it's been stuck for %d minutes)", int(store.StaleRunAfter.Minutes()))
	} else if err != nil {
		return err
	}
	return s.respondFresh(w, r, b.ID, imp.ID, http.StatusOK)
}

func (s *Service) respondFresh(w http.ResponseWriter, r *http.Request, brandID, id uuid.UUID, status int) error {
	imp, err := s.store.ImportByID(r.Context(), brandID, id)
	if err != nil {
		return err
	}
	return s.writeImport(w, r, status, imp)
}
