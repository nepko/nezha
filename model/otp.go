package model

// OTPSetting 用户 TOTP 配置
type OTPSetting struct {
	Common
	UserID uint64 `json:"user_id" gorm:"uniqueIndex"`
	Secret  string `json:"-" gorm:"type:char(32)"`
	Enabled bool   `json:"enabled"`
}

func (OTPSetting) TableName() string {
	return "otp_settings"
}

// BackupCode 2FA 备用恢复码
type BackupCode struct {
	Common
	UserID uint64 `json:"user_id" gorm:"index"`
	Code    string `json:"-" gorm:"type:char(12);uniqueIndex"`
	Used    bool   `json:"used"`
}

func (BackupCode) TableName() string {
	return "backup_codes"
}
