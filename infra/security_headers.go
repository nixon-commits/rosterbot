package main

// The content policies for the two CloudFront distributions. They are
// constants in their own file so the test can assert on the same strings the
// stack ships. See the securityHeaders comment in infra.go for why each
// directive has the value it has.
const (
	dashboardCSP = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https:; " +
		"connect-src 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"object-src 'none'"

	siteCSP = "frame-ancestors 'none'; base-uri 'self'; object-src 'none'"
)
