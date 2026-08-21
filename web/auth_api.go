package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("POST /api/v1/security/password", s.changeLocalPassword)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/app/dashboard", http.StatusSeeOther)

		return
	}

	if s.sessionValid(r) {
		http.Redirect(w, r, "/app/dashboard", http.StatusSeeOther)

		return
	}

	s.renderLoginPage(w, "")
}

func (s *Server) renderLoginPage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	_ = loginPage.Execute(w, map[string]string{
		"CSRFToken": s.csrfToken,
		"Error":     message,
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/app/dashboard", http.StatusSeeOther)

		return
	}

	if !s.requireCSRF(w, r) {
		return
	}

	address := clientAddress(r)

	if s.loginRateLimited(address) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.renderLoginPage(w, "Too many attempts. Wait a few minutes and try again.")

		return
	}

	if err := parseBoundedRequestForm(w, r, 16<<10); err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderLoginPage(w, "The sign-in form could not be read.")

		return
	}

	if !s.verifyPassword(r.FormValue("password")) {
		s.recordLoginFailure(address)
		w.WriteHeader(http.StatusUnauthorized)
		s.renderLoginPage(w, "That password is not correct.")

		return
	}

	s.clearLoginFailures(address)

	token, expires, err := s.createSession()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		s.renderLoginPage(w, "A session could not be created.")

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authSessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

	http.Redirect(w, r, "/app/dashboard", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	if cookie, err := r.Cookie(authSessionCookie); err == nil && cookie.Value != "" {
		s.auth.mu.Lock()
		delete(s.auth.sessions, cookie.Value)
		s.auth.mu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name: authSessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: secureRequest(r), SameSite: http.SameSiteStrictMode,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// changeLocalPassword sets, changes, or removes the local password. Removing
// or changing requires the current password; enabling for the first time does
// not, because the request already proves loopback access to an unprotected
// workspace.
func (s *Server) changeLocalPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	if err := parseBoundedRequestForm(w, r, 16<<10); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_password_change", "The form could not be read")

		return
	}

	action := strings.TrimSpace(r.FormValue("action"))
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")

	if s.authEnabled() && !s.verifyPassword(current) {
		renderLocalAPIError(w, http.StatusForbidden, "wrong_password", "The current password is not correct")

		return
	}

	if action == "remove" {
		next = ""
	} else if next == "" {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_password_change", "A new password is required")

		return
	}

	if minutes := strings.TrimSpace(r.FormValue("session_minutes")); minutes != "" {
		value, err := strconv.Atoi(minutes)
		if err != nil || value < authMinimumSessionMinutes || value > authMaximumSessionMinutes {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_password_change",
				"Session timeout must be between "+strconv.Itoa(authMinimumSessionMinutes)+
					" and "+strconv.Itoa(authMaximumSessionMinutes)+" minutes")

			return
		}

		if err := s.svc.SaveSettings(r.Context(), map[string]string{
			authTimeoutSettingKey: strconv.Itoa(value),
		}); err != nil {
			renderLocalAPIError(w, http.StatusInternalServerError, "password_change_failed", "Could not store the session timeout")

			return
		}
	}

	if err := s.setPassword(r.Context(), next); err != nil {
		if errors.Is(err, ErrInvalidAuthChange) {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_password_change", err.Error())

			return
		}

		renderLocalAPIError(w, http.StatusInternalServerError, "password_change_failed", "Could not store the password")

		return
	}

	// Changing the credential invalidated every session, including the
	// caller's. Give a browser a fresh one when a password is now set, so
	// enabling protection does not immediately lock the operator out.
	if s.authEnabled() {
		if token, expires, sessionErr := s.createSession(); sessionErr == nil {
			http.SetCookie(w, &http.Cookie{
				Name: authSessionCookie, Value: token, Path: "/", Expires: expires,
				HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode,
			})
		}
	}

	if !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Redirect(w, r, "/app/settings?notice=Local+access+password+updated", http.StatusSeeOther)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{"enabled": s.authEnabled()}})
}

func clientAddress(r *http.Request) string {
	address := r.RemoteAddr
	if index := strings.LastIndex(address, ":"); index > 0 {
		address = address[:index]
	}

	return address
}

// secureRequest reports whether the session cookie may carry the Secure
// attribute. The default local workspace is plain HTTP on loopback, where a
// Secure cookie would simply never be sent back; behind an operator's own TLS
// proxy, or on a direct TLS listener, the attribute is set so the session
// token can never leak over a downgraded connection.
func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
