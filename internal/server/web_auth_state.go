package server

import "net/http"

type webAuthenticationState struct {
	webAccounts          *webAccountStore
	webAccountsError     error
	webSessions          *webSessionStore
	webSessionsError     error
	webExternalAuth      *webExternalAuthRuntime
	webExternalAuthError error
}

func newWebAuthenticationState(
	options Options,
	linkAuth *synonLinkAuthenticator,
	httpClient *http.Client,
) webAuthenticationState {
	state := webAuthenticationState{}
	state.webExternalAuth, state.webExternalAuthError = newWebExternalAuthRuntime(options.WebAuth, httpClient)
	if state.webExternalAuth == nil {
		state.webExternalAuth, _ = newWebExternalAuthRuntime(WebAuthOptions{}, httpClient)
	}
	passwordEnabled := linkAuth != nil && linkAuth.enabled
	if !passwordEnabled && !state.webExternalAuth.enabled() {
		return state
	}
	bootstrapUsername := ""
	if linkAuth != nil {
		bootstrapUsername = linkAuth.username
	}
	state.webAccounts, state.webAccountsError = openWebAccountStore(options.FileRoot, bootstrapUsername)
	state.webSessions, state.webSessionsError = openWebSessionStore(options.FileRoot)
	return state
}
