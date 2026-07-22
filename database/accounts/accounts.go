package accounts

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const constantSalt = "06Wm4Jv1Hkxx"

// hashPasswdBcrypt 对密码进行 bcrypt 哈希
func hashPasswdBcrypt(passwd string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(passwd), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// CheckPassword 检查密码是否正确
//
// 如果密码正确，返回用户的 UUID 和 true；否则返回空字符串和 false
func CheckPassword(username, passwd string) (uuid string, success bool) {
	db := dbcore.GetDBInstance()
	var user models.User
	result := db.Where("username = ?", username).First(&user)
	if result.Error != nil {
		// 静默处理错误，不显示日志
		return "", false
	}

	// 检查是否为 bcrypt 哈希值
	if strings.HasPrefix(user.Passwd, "$2a$") || strings.HasPrefix(user.Passwd, "$2b$") || strings.HasPrefix(user.Passwd, "$2y$") {
		err := bcrypt.CompareHashAndPassword([]byte(user.Passwd), []byte(passwd))
		if err != nil {
			return "", false
		}
		return user.UUID, true
	}

	// 兼容旧的单轮加盐 SHA-256 哈希值
	if hashPasswd(passwd) != user.Passwd {
		return "", false
	}

	// 验证成功，在此处平滑迁移旧哈希密码至 Bcrypt 哈希
	newHash, err := hashPasswdBcrypt(passwd)
	if err == nil {
		_ = db.Model(&models.User{}).Where("uuid = ?", user.UUID).Update("passwd", newHash)
	}

	return user.UUID, true
}

// ForceResetPassword 强制重置用户密码
func ForceResetPassword(username, passwd string) (err error) {
	db := dbcore.GetDBInstance()
	hashedPassword, err := hashPasswdBcrypt(passwd)
	if err != nil {
		return err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Select("uuid").Where("username = ?", username).First(&user).Error; err != nil {
			return fmt.Errorf("无法找到用户名: %w", err)
		}
		if err := tx.Model(&models.User{}).Where("uuid = ?", user.UUID).Update("passwd", hashedPassword).Error; err != nil {
			return err
		}
		return tx.Where("uuid = ?", user.UUID).Delete(&models.Session{}).Error
	})
	if err != nil {
		return err
	}
	clearSessionCredentials()
	clearDefaultSessionActivity()
	return nil
}

// hashPasswd 对密码进行加盐哈希
func hashPasswd(passwd string) string {
	saltedPassword := passwd + constantSalt
	hash := sha256.New()
	hash.Write([]byte(saltedPassword))
	hashedPassword := base64.StdEncoding.EncodeToString(hash.Sum(nil))
	return hashedPassword
}

func CreateAccount(username, passwd string) (user models.User, err error) {
	db := dbcore.GetDBInstance()
	hashedPassword, err := hashPasswdBcrypt(passwd)
	if err != nil {
		return models.User{}, err
	}
	user = models.User{
		UUID:     uuid.New().String(),
		Username: username,
		Passwd:   hashedPassword,
	}
	err = db.Create(&user).Error
	if err != nil {
		return models.User{}, err
	}
	return user, nil
}

func DeleteAccountByUsername(username string) (err error) {
	db := dbcore.GetDBInstance()
	err = db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		result := tx.Select("uuid").Where("username = ?", username).First(&user)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Where("uuid = ?", user.UUID).Delete(&models.Session{}).Error; err != nil {
			return err
		}
		return tx.Where("uuid = ?", user.UUID).Delete(&models.User{}).Error
	})
	if err != nil {
		return err
	}
	clearSessionCredentials()
	clearDefaultSessionActivity()
	return nil
}

// 创建默认管理员账户，使用环境变量 ADMIN_USERNAME 作为用户名，环境变量 ADMIN_PASSWORD 作为密码
func CreateDefaultAdminAccount() (username, passwd string, err error) {
	db := dbcore.GetDBInstance()

	username = os.Getenv("ADMIN_USERNAME")
	if username == "" {
		username = "admin"
	}

	passwd = os.Getenv("ADMIN_PASSWORD")
	if passwd == "" {
		passwd = utils.GeneratePassword()
	}

	hashedPassword, err := hashPasswdBcrypt(passwd)
	if err != nil {
		return "", "", err
	}

	user := models.User{
		UUID:      uuid.New().String(),
		Username:  username,
		Passwd:    hashedPassword,
		SSOID:     "",
		CreatedAt: models.FromTime(time.Now()),
		UpdatedAt: models.FromTime(time.Now()),
	}

	err = db.Create(&user).Error
	if err != nil {
		return "", "", err
	}

	return username, passwd, nil
}

func GetUserByUUID(uuid string) (user models.User, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&user).Error
	if err != nil {
		return models.User{}, err
	}
	return user, nil
}

// 通过 SSO 信息获取用户
func GetUserBySSO(ssoID string) (user models.User, err error) {
	db := dbcore.GetDBInstance()

	// 首先尝试查找已存在的用户
	err = db.Where("sso_id = ?", ssoID).First(&user).Error
	if err == nil {
		return user, nil
	}

	// 如果找不到用户，返回明确的错误信息
	return models.User{}, fmt.Errorf("用户不存在：%s", ssoID)
}

func BindingExternalAccount(uuid string, sso_id string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Update("sso_id", sso_id).Error
	if err != nil {
		return err
	}
	return nil
}

func UnbindExternalAccount(uuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Update("sso_id", "").Error
	if err != nil {
		return err
	}
	return nil
}

func UpdateUser(uuid string, name, password, sso_type *string) error {
	db := dbcore.GetDBInstance()
	updates := make(map[string]interface{})
	if name != nil {
		updates["username"] = *name
	}
	if password != nil {
		hashed, err := hashPasswdBcrypt(*password)
		if err != nil {
			return err
		}
		updates["passwd"] = hashed
	}
	if sso_type != nil {
		updates["sso_type"] = *sso_type
	}
	updates["updated_at"] = time.Now()
	err := db.Transaction(func(tx *gorm.DB) error {
		var existingUser models.User
		if err := tx.Select("uuid").Where("uuid = ?", uuid).First(&existingUser).Error; err != nil {
			return fmt.Errorf("user not found: %s", uuid)
		}
		if err := tx.Model(&models.User{}).Where("uuid = ?", uuid).Updates(updates).Error; err != nil {
			return err
		}
		if password != nil {
			return tx.Where("uuid = ?", uuid).Delete(&models.Session{}).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	if password != nil {
		clearSessionCredentials()
		clearDefaultSessionActivity()
	}
	return nil
}
