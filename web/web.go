package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

//go:embed static
var static embed.FS

type Server struct {
	tmpl         map[string]*template.Template
	srv          *http.Server
	svc          *Service
	csrfToken    string
	systemProbe  localSystemProbe
	apiRates     apiRateState
	apiRateLimit apiAtomicInt64
	auth         authState
}

func New(svc *Service, addr string) (*Server, error) {
	if wildcardBind(addr) {
		log.Printf("WARNING: web server is listening on all interfaces at %s; use a firewall and authentication outside a trusted local machine", addr)
	}

	csrfToken, err := newCSRFToken()
	if err != nil {
		return nil, err
	}

	ans := Server{
		svc:         svc,
		tmpl:        make(map[string]*template.Template),
		csrfToken:   csrfToken,
		systemProbe: newDefaultLocalSystemProbe(),
		srv: &http.Server{
			Addr:              addr,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			// Streaming job events can remain open for the life of a scrape.
			// Individual non-streaming handlers still bound their own work.
			WriteTimeout:   0,
			IdleTimeout:    120 * time.Second,
			MaxHeaderBytes: 1 << 20,
		},
	}
	ans.initializeAPIAccessSettings()
	ans.initializeAuth()

	staticFS, err := fs.Sub(static, "static")
	if err != nil {
		return nil, err
	}

	fileServer := http.FileServer(http.FS(staticFS))
	mux := http.NewServeMux()

	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))
	mux.HandleFunc("/scrape", ans.scrape)
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.download(w, r)
	})
	mux.HandleFunc("/delete", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.delete(w, r)
	})
	mux.HandleFunc("/jobs", ans.getJobs)
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.viewJob(w, r)
	})
	mux.HandleFunc("/legacy", ans.index)
	mux.HandleFunc("GET /{$}", ans.dashboardPage)
	mux.HandleFunc("GET /app/dashboard", ans.dashboardPage)
	mux.HandleFunc("GET /app/scrapes/new", ans.newScrapePage)
	mux.HandleFunc("POST /app/scrapes", ans.createScrapeFromWizard)
	mux.HandleFunc("GET /app/jobs", ans.jobsPage)
	mux.HandleFunc("GET /app/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.jobMonitorPage(w, r)
	})
	mux.HandleFunc("GET /app/jobs/{id}/benchmark", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.jobBenchmarkPage(w, r)
	})
	mux.HandleFunc("GET /app/map", ans.mapPage)
	mux.HandleFunc("GET /app/results", ans.resultsPage)
	mux.HandleFunc("GET /app/results/{id}", ans.businessDetailPage)
	mux.HandleFunc("GET /app/results/{id}/drawer", ans.businessDetailDrawer)
	mux.HandleFunc("GET /app/system", ans.systemPage)
	mux.HandleFunc("GET /app/api", ans.apiWorkspacePage)
	mux.HandleFunc("GET /app/settings", ans.settingsPage)
	mux.HandleFunc("GET /app/exports", ans.exportsPage)
	mux.HandleFunc("GET /app/saved-searches", ans.reusablePage)
	mux.HandleFunc("GET /app/schedules", ans.schedulesPage)
	mux.HandleFunc("GET /app/proxies", ans.proxiesPage)
	mux.HandleFunc("GET /app/onboarding", ans.onboardingPage)

	// api routes
	mux.HandleFunc("/api/docs", ans.redocHandler)
	mux.HandleFunc("GET /api/openapi.json", ans.apiOpenAPI)
	mux.HandleFunc("/api/v1/dashboard", ans.apiDashboard)
	mux.HandleFunc("POST /api/v1/jobs/validate", ans.apiValidateJob)
	mux.HandleFunc("/api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			ans.apiScrape(w, r)
		case http.MethodGet:
			ans.apiGetJobs(w, r)
		default:
			ans := apiError{
				Code:    http.StatusMethodNotAllowed,
				Message: "Method not allowed",
			}

			renderJSON(w, http.StatusMethodNotAllowed, ans)
		}
	})

	mux.HandleFunc("/api/v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		switch r.Method {
		case http.MethodGet:
			ans.apiGetJob(w, r)
		case http.MethodDelete:
			ans.apiDeleteJob(w, r)
		default:
			ans := apiError{
				Code:    http.StatusMethodNotAllowed,
				Message: "Method not allowed",
			}

			renderJSON(w, http.StatusMethodNotAllowed, ans)
		}
	})

	mux.HandleFunc("/api/v1/jobs/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		if r.Method != http.MethodGet {
			ans := apiError{
				Code:    http.StatusMethodNotAllowed,
				Message: "Method not allowed",
			}

			renderJSON(w, http.StatusMethodNotAllowed, ans)

			return
		}

		ans.download(w, r)
	})
	mux.HandleFunc("/api/v1/jobs/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.apiJobProgress(w, r)
	})
	mux.HandleFunc("/api/v1/jobs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.apiJobEvents(w, r)
	})
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		ans.jobLogsDownload(w, r)
	})
	ans.registerLifecycleRoutes(mux)
	ans.registerScrapePlanRoutes(mux)
	ans.registerTemplateMetricRoutes(mux)
	ans.registerJobOrganisationRoutes(mux)
	ans.registerJobLabelRoutes(mux)
	ans.registerRetentionRoutes(mux)
	ans.registerAuthRoutes(mux)
	ans.registerKeywordSetRoutes(mux)
	ans.registerProxyRetestRoutes(mux)
	ans.registerScheduleAutomationRoutes(mux)
	ans.registerManualEditRoutes(mux)
	ans.registerLiveControlRoutes(mux)
	ans.registerScreenshotRoutes(mux)
	ans.registerTemplateRenameRoutes(mux)
	ans.registerProspectRoutes(mux)
	ans.registerCheckpointRoutes(mux)
	ans.registerBenchmarkRoutes(mux)
	ans.registerCoverageRoutes(mux)
	ans.registerRerunRoutes(mux)
	ans.registerCampaignTemplateRoutes(mux)
	ans.registerExportProfileRoutes(mux)
	ans.registerResultRoutes(mux)
	ans.registerResultListRoutes(mux)
	ans.registerDuplicateRoutes(mux)
	ans.registerEnrichmentRoutes(mux)
	ans.registerMapRoutes(mux)
	ans.registerQualityRoutes(mux)
	ans.registerExportRoutes(mux)
	ans.registerIntegrationRoutes(mux)
	ans.registerAPIAccessRoutes(mux)
	ans.registerGlobalSearchRoutes(mux)
	ans.registerLocalAIRoutes(mux)
	mux.HandleFunc("GET /api/v1/system/health", ans.apiSystemHealth)
	mux.HandleFunc("GET /api/v1/system/metrics", ans.apiSystemMetrics)
	mux.HandleFunc("POST /api/v1/system/self-test", ans.apiSystemSelfTest)
	mux.HandleFunc("POST /api/v1/system/integrity", ans.apiSystemIntegrity)
	mux.HandleFunc("POST /api/v1/system/vacuum", ans.apiSystemVacuum)
	mux.HandleFunc("POST /api/v1/system/backups", ans.apiSystemBackup)
	mux.HandleFunc("GET /api/v1/system/backups/{id}/download", ans.downloadSystemBackup)
	mux.HandleFunc("POST /api/v1/system/cache/clear", ans.apiSystemClearCache)
	mux.HandleFunc("POST /api/v1/system/artifacts/cleanup", ans.apiSystemCleanArtifacts)
	mux.HandleFunc("POST /api/v1/system/jobs/stop-all", ans.apiSystemStopAll)
	mux.HandleFunc("GET /api/v1/system/diagnostics/download", ans.downloadSystemDiagnostics)
	mux.HandleFunc("GET /api/v1/system/update-info", ans.apiSystemUpdateInfo)
	mux.HandleFunc("POST /api/v1/settings", ans.saveSettings)
	mux.HandleFunc("POST /api/v1/saved-views", ans.saveResultView)
	mux.HandleFunc("POST /api/v1/saved-views/{id}/delete", ans.deleteSavedResultView)
	mux.HandleFunc("POST /api/v1/templates/import", ans.importScrapeTemplate)
	mux.HandleFunc("GET /api/v1/templates/{id}/export", ans.exportScrapeTemplate)
	mux.HandleFunc("POST /api/v1/templates/{id}/pin", ans.pinScrapeTemplate)
	mux.HandleFunc("POST /api/v1/templates/{id}/duplicate", ans.duplicateScrapeTemplate)
	mux.HandleFunc("POST /api/v1/templates/{id}/delete", ans.deleteScrapeTemplate)
	mux.HandleFunc("POST /api/v1/schedules", ans.createSchedule)
	mux.HandleFunc("POST /api/v1/schedules/{id}/run", ans.runScheduleNow)
	mux.HandleFunc("POST /api/v1/schedules/{id}/{action}", ans.toggleSchedule)
	mux.HandleFunc("POST /api/v1/schedules/{id}/delete", ans.deleteSchedule)
	mux.HandleFunc("POST /api/v1/proxy-pools/import", ans.importProxyPool)
	mux.HandleFunc("POST /api/v1/proxy-pools/{id}/delete", ans.deleteProxyPool)
	ans.registerProxyTestRoutes(mux)
	mux.HandleFunc("POST /api/v1/proxies/{id}/test", ans.testProxy)
	mux.HandleFunc("POST /api/v1/proxies/{id}/{action}", ans.mutateProxy)
	mux.HandleFunc("POST /api/v1/onboarding/complete", ans.completeOnboarding)
	mux.HandleFunc("POST /api/v1/onboarding/self-test", ans.runOnboardingSelfTest)

	handler := securityHeaders(ans.localAuthentication(ans.apiAccessMiddleware(ans.browserCSRFProtection(mux))))
	ans.srv.Handler = handler

	tmplsKeys := []string{
		"static/templates/index.html",
		"static/templates/job_rows.html",
		"static/templates/job_row.html",
		"static/templates/job_view.html",
		"static/templates/redoc.html",
	}

	for _, key := range tmplsKeys {
		tmp, err := template.ParseFS(static, key)
		if err != nil {
			return nil, err
		}

		ans.tmpl[key] = tmp
	}

	if err := ans.loadAppTemplates(); err != nil {
		return nil, err
	}

	return &ans, nil
}

func wildcardBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.HasPrefix(addr, ":")
	}

	return host == "" || host == "0.0.0.0" || host == "::"
}

func (s *Server) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		err := s.srv.Shutdown(context.Background())
		if err != nil {
			log.Println(err)

			return
		}

		log.Println("server stopped")
	}()

	fmt.Fprintf(os.Stderr, "visit http://localhost%s\n", s.srv.Addr)

	err := s.srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

type formData struct {
	Name       string
	MaxTime    string
	Keywords   []string
	Language   string
	Zoom       int
	FastMode   bool
	Radius     int
	Lat        string
	Lon        string
	Depth      int
	Email      bool
	Proxies    []string
	GridBBox   string
	GridCellKM float64
}

type ctxKey string

const idCtxKey ctxKey = "id"

func requestWithID(r *http.Request) *http.Request {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}

	parsed, err := uuid.Parse(id)
	if err == nil {
		r = r.WithContext(context.WithValue(r.Context(), idCtxKey, parsed))
	}

	return r
}

func getIDFromRequest(r *http.Request) (uuid.UUID, bool) {
	id, ok := r.Context().Value(idCtxKey).(uuid.UUID)

	return id, ok
}

//nolint:gocritic // this is used in template
func (f formData) ProxiesString() string {
	return strings.Join(f.Proxies, "\n")
}

//nolint:gocritic // this is used in template
func (f formData) KeywordsString() string {
	return strings.Join(f.Keywords, "\n")
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	tmpl, ok := s.tmpl["static/templates/index.html"]
	if !ok {
		http.Error(w, "missing tpl", http.StatusInternalServerError)

		return
	}

	data := formData{
		Name:       "",
		MaxTime:    "30m",
		Keywords:   []string{},
		Language:   "en",
		Zoom:       15,
		FastMode:   false,
		Radius:     10000,
		Lat:        "0",
		Lon:        "0",
		Depth:      10,
		Email:      false,
		GridCellKM: 1,
	}

	_ = tmpl.Execute(w, data)
}

func (s *Server) scrape(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	err := r.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	newJob := Job{
		ID:     uuid.New().String(),
		Name:   r.Form.Get("name"),
		Date:   time.Now().UTC(),
		Status: StatusPending,
		Data:   JobData{},
	}

	maxTimeStr := r.Form.Get("maxtime")

	maxTime, err := time.ParseDuration(maxTimeStr)
	if err != nil {
		http.Error(w, "invalid max time", http.StatusUnprocessableEntity)

		return
	}

	if maxTime < time.Minute*3 {
		http.Error(w, "max time must be more than 3m", http.StatusUnprocessableEntity)

		return
	}

	newJob.Data.MaxTime = maxTime

	keywordsStr, ok := r.Form["keywords"]
	if !ok {
		http.Error(w, "missing keywords", http.StatusUnprocessableEntity)

		return
	}

	keywords := strings.Split(keywordsStr[0], "\n")
	for _, k := range keywords {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}

		newJob.Data.Keywords = append(newJob.Data.Keywords, k)
	}

	newJob.Data.Lang = r.Form.Get("lang")

	newJob.Data.Zoom, err = strconv.Atoi(r.Form.Get("zoom"))
	if err != nil {
		http.Error(w, "invalid zoom", http.StatusUnprocessableEntity)

		return
	}

	if r.Form.Get("fastmode") == "on" {
		newJob.Data.FastMode = true
	}

	newJob.Data.Radius, err = strconv.Atoi(r.Form.Get("radius"))
	if err != nil {
		http.Error(w, "invalid radius", http.StatusUnprocessableEntity)

		return
	}

	newJob.Data.Lat = r.Form.Get("latitude")
	newJob.Data.Lon = r.Form.Get("longitude")

	newJob.Data.Depth, err = strconv.Atoi(r.Form.Get("depth"))
	if err != nil {
		http.Error(w, "invalid depth", http.StatusUnprocessableEntity)

		return
	}

	newJob.Data.Email = r.Form.Get("email") == "on"
	newJob.Data.GridBBox = strings.TrimSpace(r.Form.Get("grid_bbox"))

	if newJob.Data.GridBBox != "" {
		newJob.Data.GridCellKM, err = strconv.ParseFloat(r.Form.Get("grid_cell_km"), 64)
		if err != nil {
			http.Error(w, "invalid grid cell size", http.StatusUnprocessableEntity)

			return
		}
	}

	proxies := strings.Split(r.Form.Get("proxies"), "\n")
	if len(proxies) > 0 {
		for _, p := range proxies {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}

			newJob.Data.Proxies = append(newJob.Data.Proxies, p)
		}
	}

	err = newJob.Validate()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)

		return
	}

	err = s.svc.Create(r.Context(), &newJob)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	tmpl, ok := s.tmpl["static/templates/job_row.html"]
	if !ok {
		http.Error(w, "missing tpl", http.StatusInternalServerError)

		return
	}

	_ = tmpl.Execute(w, newJob)
}

func (s *Server) getJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	tmpl, ok := s.tmpl["static/templates/job_rows.html"]
	if !ok {
		http.Error(w, "missing tpl", http.StatusInternalServerError)
		return
	}

	jobs, err := s.svc.All(context.Background())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	_ = tmpl.Execute(w, jobs)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	ctx := r.Context()

	id, ok := getIDFromRequest(r)
	if !ok {
		http.Error(w, "Invalid ID", http.StatusUnprocessableEntity)

		return
	}

	filePath, err := s.svc.GetCSV(ctx, id.String())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "Failed to open file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	fileName := filepath.Base(filePath)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
	w.Header().Set("Content-Type", "text/csv")

	_, err = io.Copy(w, file)
	if err != nil {
		http.Error(w, "Failed to send file", http.StatusInternalServerError)
		return
	}
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	deleteID, ok := getIDFromRequest(r)
	if !ok {
		http.Error(w, "Invalid ID", http.StatusUnprocessableEntity)

		return
	}

	err := s.svc.Delete(r.Context(), deleteID.String())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.WriteHeader(http.StatusOK)
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type apiScrapeRequest struct {
	Name string
	JobData
}

type apiScrapeResponse struct {
	ID string `json:"id"`
}

func (s *Server) redocHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/app/api", http.StatusSeeOther)
}

func (s *Server) apiScrape(w http.ResponseWriter, r *http.Request) {
	var req apiScrapeRequest

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		ans := apiError{
			Code:    http.StatusUnprocessableEntity,
			Message: err.Error(),
		}

		renderJSON(w, http.StatusUnprocessableEntity, ans)

		return
	}

	newJob := Job{
		ID:     uuid.New().String(),
		Name:   req.Name,
		Date:   time.Now().UTC(),
		Status: StatusPending,
		Data:   req.JobData,
	}

	// convert to seconds
	newJob.Data.MaxTime *= time.Second

	err = newJob.Validate()
	if err != nil {
		ans := apiError{
			Code:    http.StatusUnprocessableEntity,
			Message: err.Error(),
		}

		renderJSON(w, http.StatusUnprocessableEntity, ans)

		return
	}

	err = s.svc.Create(r.Context(), &newJob)
	if err != nil {
		ans := apiError{
			Code:    http.StatusInternalServerError,
			Message: err.Error(),
		}

		renderJSON(w, http.StatusInternalServerError, ans)

		return
	}

	ans := apiScrapeResponse{
		ID: newJob.ID,
	}

	renderJSON(w, http.StatusCreated, ans)
}

func (s *Server) apiGetJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.svc.All(r.Context())
	if err != nil {
		apiError := apiError{
			Code:    http.StatusInternalServerError,
			Message: err.Error(),
		}

		renderJSON(w, http.StatusInternalServerError, apiError)

		return
	}

	renderJSON(w, http.StatusOK, sanitizedJobsForAPI(jobs))
}

func (s *Server) apiGetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := getIDFromRequest(r)
	if !ok {
		apiError := apiError{
			Code:    http.StatusUnprocessableEntity,
			Message: "Invalid ID",
		}

		renderJSON(w, http.StatusUnprocessableEntity, apiError)

		return
	}

	job, err := s.svc.Get(r.Context(), id.String())
	if err != nil {
		apiError := apiError{
			Code:    http.StatusNotFound,
			Message: http.StatusText(http.StatusNotFound),
		}

		renderJSON(w, http.StatusNotFound, apiError)

		return
	}

	renderJSON(w, http.StatusOK, sanitizedJobForAPI(job))
}

// viewJob renders the map modal fragment for a job, embedding the job's places
// directly so the client needs no separate data request.
func (s *Server) viewJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		http.Error(w, "Invalid ID", http.StatusUnprocessableEntity)

		return
	}

	places, err := s.svc.GetPlaces(r.Context(), id.String())

	if err != nil {
		if !errors.Is(err, ErrPlacesNotFound) {
			log.Printf("view job %s: %v", id, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}

		// No CSV yet: render the modal with an empty state rather than an error.
		places = []Place{}
	}

	tmpl, ok := s.tmpl["static/templates/job_view.html"]
	if !ok {
		http.Error(w, "missing tpl", http.StatusInternalServerError)

		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, places); err != nil {
		log.Printf("view job %s: render: %v", id, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	_, _ = buf.WriteTo(w)
}

func (s *Server) apiDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, ok := getIDFromRequest(r)
	if !ok {
		apiError := apiError{
			Code:    http.StatusUnprocessableEntity,
			Message: "Invalid ID",
		}

		renderJSON(w, http.StatusUnprocessableEntity, apiError)

		return
	}

	err := s.svc.Delete(r.Context(), id.String())
	if err != nil {
		apiError := apiError{
			Code:    http.StatusInternalServerError,
			Message: err.Error(),
		}

		renderJSON(w, http.StatusInternalServerError, apiError)

		return
	}

	w.WriteHeader(http.StatusOK)
}

func renderJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(data)
}

func formatDate(t time.Time) string {
	return t.Format("Jan 02, 2006 15:04:05")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", frameOptionsForPath(r.URL.Path))
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", localContentSecurityPolicy(r.URL.Path))
		if strings.HasPrefix(r.URL.Path, "/app/") || strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}

		next.ServeHTTP(w, r)
	})
}

// browserCSRFProtection applies one policy to every versioned API mutation:
// requests carrying a browser Origin must prove that they came from a page
// rendered by this process. Local command-line clients historically omit
// Origin, so their loopback API behavior remains compatible.
func (s *Server) browserCSRFProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && isMutationMethod(r.Method) &&
			r.Header.Get("Origin") != "" && !s.requireCSRF(w, r) {
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isMutationMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func localContentSecurityPolicy(path string) string {
	if path == "/legacy" || path == "/view" || path == "/jobs" || path == "/scrape" {
		// The preserved legacy UI still loads HTMX, Leaflet, and map tiles from
		// their historical providers. The local application under /app has a
		// deliberately tighter, dependency-free policy below.
		return "default-src 'self'; " +
			"script-src 'self' cdn.redoc.ly cdnjs.cloudflare.com 'unsafe-inline'; " +
			"style-src 'self' 'unsafe-inline' fonts.googleapis.com cdnjs.cloudflare.com; " +
			"img-src 'self' data: cdn.redoc.ly cdnjs.cloudflare.com *.tile.openstreetmap.org; " +
			"font-src 'self' fonts.gstatic.com; connect-src 'self'; " +
			"object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
	}

	return "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
		"base-uri 'self'; frame-ancestors " + frameAncestorsForPath(path) + "; form-action 'self'"
}

// framableLocalPaths are the only local application pages another page of this
// same application embeds. The Results split view frames Map Explorer so the
// table and the map share one filter set.
func framableLocalPath(path string) bool {
	return path == "/app/map"
}

// frameOptionsForPath keeps clickjacking protection at DENY everywhere except
// the pages this application frames itself, which allow same-origin embedding
// only. Cross-origin framing stays blocked in both cases.
func frameOptionsForPath(path string) string {
	if framableLocalPath(path) {
		return "SAMEORIGIN"
	}

	return "DENY"
}

func frameAncestorsForPath(path string) string {
	if framableLocalPath(path) {
		return "'self'"
	}

	return "'none'"
}
