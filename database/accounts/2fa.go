package accounts

import (
	"context"
	"image"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage"
	"github.com/pquerna/otp/totp"
)

var (
	TwoFactorIssuer = "Komari Monitor"
)

func Generate2Fa() (string, image.Image, error) {
	otp, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TwoFactorIssuer,
		AccountName: "komari",
	})
	if err != nil {
		return "", nil, err
	}
	img, err := otp.Image(250, 250)
	if err != nil {
		return "", nil, err
	}
	return otp.Secret(), img, nil
}

func Enable2Fa(uuid, secret string) error {
	if err := updateExternalUserAuth(uuid, false, func(user *models.User) {
		user.TwoFactor = secret
	}); err != nil {
		return err
	}
	db := dbcore.GetDBInstance()
	return db.Model(&models.User{}).Where("uuid = ?", uuid).Update("two_factor", secret).Error
}

func Verify2Fa(uuid, code string) (bool, error) {
	var user models.User
	var err error
	if store, ok := storage.Control(); ok {
		user, err = store.UserByUUID(context.Background(), uuid)
	} else {
		err = dbcore.GetDBInstance().Where("uuid = ?", uuid).First(&user).Error
	}
	if err != nil {
		return false, err
	}

	if user.TwoFactor == "" {
		return false, nil // 用户未启用2FA
	}

	valid := totp.Validate(code, user.TwoFactor)
	if !valid {
		return false, nil
	}

	return true, nil
}

func Disable2Fa(uuid string) error {
	if err := updateExternalUserAuth(uuid, false, func(user *models.User) {
		user.TwoFactor = ""
	}); err != nil {
		return err
	}
	db := dbcore.GetDBInstance()
	return db.Model(&models.User{}).Where("uuid = ?", uuid).Update("two_factor", "").Error
}
