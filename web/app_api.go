package web

import (
	"net/http"
	"strings"
)

type apiWorkspacePageData struct {
	BaseURL               string
	AuthenticationSummary string
	ExposedBeyondLoopback bool
	Groups                []apiGroupView
	OperationCount        int
	Examples              []codeExample
	APIKeys               []APIKeyRecord
	Integrations          []IntegrationRecord
	Deliveries            []IntegrationDelivery
	EventNames            []string
	RequestLogs           []APIRequestLog
	RateLimit             int64
	SpecVersion           string
	APIVersion            string
	Notice                string
}

// apiGroupView is one collapsible endpoint-group section on the reference page.
type apiGroupView struct {
	Name       string
	Slug       string
	Operations []apiEndpointView
}

type apiEndpointView struct {
	Method      string
	Path        string
	Description string
	Anchor      string
	Mutating    bool
	Examples    []codeExample
}

// maximumRenderedDeliveries bounds the delivery history table on the page.
const maximumRenderedDeliveries = 50

func (s *Server) apiWorkspacePage(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		host = s.srv.Addr
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	baseURL := scheme + "://" + host
	activity, _ := s.appActivity(r)
	page := apiWorkspacePageData{
		BaseURL:               baseURL,
		AuthenticationSummary: "Loopback-compatible API keys with read-only and full-access permissions",
		ExposedBeyondLoopback: wildcardBind(s.srv.Addr),
		RateLimit:             s.apiRateLimit.Load(),
		SpecVersion:           openAPIVersion,
		APIVersion:            localAPIVersion,
		Notice:                strings.TrimSpace(r.URL.Query().Get("notice")),
		Groups:                apiReferenceGroups(baseURL),
		Examples:              localAPIExamples(resultsSearchOperation(), baseURL),
	}
	page.OperationCount = len(localAPICatalogue())
	page.APIKeys, _ = s.svc.ListAPIKeys(r.Context(), 100)
	page.Integrations, _ = s.svc.ListIntegrations(r.Context(), false, maximumIntegrations)
	page.Deliveries, _ = s.svc.ListIntegrationDeliveries(r.Context(), "", maximumRenderedDeliveries)
	page.EventNames = integrationEventNames()
	page.RequestLogs, _ = s.svc.ListAPIRequestLogs(r.Context(), 50)

	s.renderAppPage(w, "api", appPageData{
		Title:     "Local API",
		Subtitle:  "Use the versioned interface from scripts running on this computer.",
		ActiveNav: "api",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

// apiReferenceGroups renders the generated catalogue for the browsable page,
// attaching the four language examples to every operation.
func apiReferenceGroups(baseURL string) []apiGroupView {
	groups := localAPIGroups()
	views := make([]apiGroupView, 0, len(groups))
	for _, group := range groups {
		view := apiGroupView{Name: group.Name, Slug: anchorSlug(group.Name)}
		for _, operation := range group.Operations {
			view.Operations = append(view.Operations, apiEndpointView{
				Method:      operation.Method,
				Path:        operation.Path,
				Description: operation.Summary,
				Anchor:      operation.OperationID(),
				Mutating:    operation.Mutating(),
				Examples:    localAPIExamples(operation, baseURL),
			})
		}
		views = append(views, view)
	}

	return views
}

// resultsSearchOperation is the operation shown in the quick-start card: the
// one call an operator almost always makes first.
func resultsSearchOperation() localAPIOperation {
	for _, operation := range localAPICatalogue() {
		if operation.Method == http.MethodGet && operation.Path == "/api/v1/results" {
			return operation
		}
	}

	return localAPIOperation{Group: "Results", Method: http.MethodGet, Path: "/api/v1/results", Summary: "Search results"}
}

// anchorSlug turns a group name into a stable DOM identifier.
func anchorSlug(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(value) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		default:
			builder.WriteByte('-')
		}
	}

	return strings.Trim(builder.String(), "-")
}
