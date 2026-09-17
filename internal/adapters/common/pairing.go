package common

type PairedUser struct {
	UserID      string
	DisplayName string
	PairedAt    int64
}

type PlatformPairingConfig struct {
	AllowedUsers []string
	PairedUsers  []PairedUser
}

func IsPaired(platform string, userID string, config map[string]PlatformPairingConfig) bool {
	platformConfig, ok := config[platform]
	if !ok {
		return false
	}
	for _, allowed := range platformConfig.AllowedUsers {
		if allowed == userID {
			return true
		}
	}
	for _, paired := range platformConfig.PairedUsers {
		if paired.UserID == userID {
			return true
		}
	}
	return false
}
