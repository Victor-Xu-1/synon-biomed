//go:build !darwin

package networktls

import "context"

func detectCorporateAnchors(context.Context) (bool, error) {
	return false, nil
}
