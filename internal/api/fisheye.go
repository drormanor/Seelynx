package api

import (
	"math"
	"net/http"

	"seelynx/internal/store"
)

// fisheyeInput is the body of PUT /api/cameras/{id}/fisheye. A body with "lens": false
// says the camera has a normal lens.
type fisheyeInput struct {
	Lens bool `json:"lens"`
	store.Fisheye
}

// saveFisheye records how a camera's fisheye lens sits in its picture. It's only used
// by viewers to show a normal view, so the camera's stream carries on untouched.
func (s *Server) saveFisheye(w http.ResponseWriter, r *http.Request) {
	c, ok := s.camera(w, r)
	if !ok {
		return
	}
	var in fisheyeInput
	if !decode(w, r, &in) {
		return
	}
	if !in.Lens {
		c.Fisheye = nil
	} else {
		f := in.Fisheye
		if msg := checkFisheye(f); msg != "" {
			fail(w, http.StatusBadRequest, msg)
			return
		}
		c.Fisheye = &f
	}
	c, err := s.Store.UpdateCamera(r.Context(), c)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// saveRotation records how far viewers turn a camera's picture, for a camera mounted on
// its side or upside down. Like the lens, it leaves the camera's stream untouched.
func (s *Server) saveRotation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.camera(w, r)
	if !ok {
		return
	}
	var in struct {
		Rotation int `json:"rotation"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Rotation != 0 && in.Rotation != 90 && in.Rotation != 180 && in.Rotation != 270 {
		fail(w, http.StatusBadRequest, "The picture can be turned 0, 90, 180 or 270 degrees.")
		return
	}
	c.Rotation = in.Rotation
	c, err := s.Store.UpdateCamera(r.Context(), c)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func checkFisheye(f store.Fisheye) string {
	for _, v := range []float64{f.X, f.Y, f.R, f.FOV, f.Tilt} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "The lens settings must be numbers."
		}
	}
	switch {
	case f.Mount != store.FisheyeWall && f.Mount != store.FisheyeCeiling && f.Mount != store.FisheyeDesk:
		return "Choose how the camera is mounted: on a wall, on the ceiling or on a desk."
	case f.X < 0 || f.X > 1 || f.Y < 0 || f.Y > 1:
		return "The centre of the circle must be inside the picture."
	case f.R < 0.05 || f.R > 1.5:
		return "The circle must be between 5% and 150% of the picture's height across its radius."
	case f.FOV < 120 || f.FOV > 240:
		return "The lens angle must be 120 to 240 degrees."
	case f.Tilt < -60 || f.Tilt > 60:
		return "The tilt must be -60 to 60 degrees."
	}
	return ""
}
