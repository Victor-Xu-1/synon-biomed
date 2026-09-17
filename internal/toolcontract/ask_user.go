package toolcontract

const AskUser = "ask_user"

var askUserAliases = [...]string{
	AskUser,
	"AskUserQuestion",
	"ask_user_question",
}

// CanonicalAskUser accepts only the exact durable names that have existed for
// the AskUser tool. Callers must not trim or case-fold before invoking it.
func CanonicalAskUser(name string) (string, bool) {
	for _, alias := range askUserAliases {
		if name == alias {
			return AskUser, true
		}
	}
	return "", false
}
