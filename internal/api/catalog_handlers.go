package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-103/internal/catalog"
)

func (s *Server) getCatalog(writer http.ResponseWriter, _ *http.Request) {
	snapshot, err := s.services.Catalog.Current()
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (s *Server) getCatalogLine(writer http.ResponseWriter, request *http.Request) {
	view, err := s.services.Catalog.Line(chi.URLParam(request, "id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) applyCatalog(writer http.ResponseWriter, request *http.Request) {
	var input CatalogConfigurationRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	configuration := catalog.Configuration{
		Generation: input.Generation,
		Stations:   input.Stations,
		Segments:   input.Segments,
		Paths:      input.Paths,
		AppliedAt:  s.services.Clock.Now(),
	}
	snapshot, err := s.services.Catalog.Apply(configuration)
	if err == nil {
		if persistErr := s.services.Persist("catalog-configuration", "generation", snapshot); persistErr != nil {
			writeError(writer, http.StatusInternalServerError, persistErr)
			return
		}
		writeJSON(writer, http.StatusCreated, snapshot)
		return
	}
	var invalid *catalog.ValidationError
	if errors.As(err, &invalid) {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	// Duplicate/skipped generation conflicts, not malformed content.
	writeError(writer, http.StatusConflict, err)
}
