package plugin

import "net/http"

// RouteRegistrar is the authorization-aware surface handed to plugin route
// registrars. Plugins never receive the raw *http.ServeMux, so every plugin
// route must declare its access level and the core enforces it (AGENTS §4);
// providers still own their own routes (AGENTS §1).
type RouteRegistrar interface {
	// Admin registers a route reachable only by authenticated admins.
	Admin(method, path string, h http.HandlerFunc)
	// User registers a route reachable by any authenticated caller.
	User(method, path string, h http.HandlerFunc)
}
