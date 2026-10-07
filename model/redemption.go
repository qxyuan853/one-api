package model

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/helper"
)

const (
	RedemptionCodeStatusEnabled  = 1 // don't use 0, 0 is the default value!
	RedemptionCodeStatusDisabled = 2 // also don't use 0
	RedemptionCodeStatusUsed     = 3 // also don't use 0
)

type Redemption struct {
	Id           int    `json:"id"`
	UserId       int    `json:"user_id"`
	Key          string `json:"key" gorm:"type:char(32);uniqueIndex"`
	Status       int    `json:"status" gorm:"default:1"`
	Name         string `json:"name" gorm:"index"`
	Quota        int64  `json:"quota" gorm:"bigint;default:100"`
	CreatedTime  int64  `json:"created_time" gorm:"bigint"`
	RedeemedTime int64  `json:"redeemed_time" gorm:"bigint"`
	Count        int    `json:"count" gorm:"-:all"` // only for api request
}

func GetAllRedemptions(startIdx int, num int) ([]*Redemption, error) {
	var redemptions []*Redemption
	var err error
	err = DB.Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error
	return redemptions, err
}

func SearchRedemptions(keyword string) (redemptions []*Redemption, err error) {
	err = DB.Where("id = ? or name LIKE ?", keyword, keyword+"%").Find(&redemptions).Error
	return redemptions, err
}

func GetRedemptionById(id int) (*Redemption, error) {
	if id == 0 {
		return nil, errors.New("id 为空！")
	}
	redemption := Redemption{Id: id}
	var err error = nil
	err = DB.First(&redemption, "id = ?", id).Error
	return &redemption, err
}

func Redeem(ctx context.Context, key string, userId int) (quota int64, err error) {
	if key == "" {
		return 0, errors.New("未提供兑换码")
	}
	if userId == 0 {
		return 0, errors.New("无效的 user id")
	}
	redemption := &Redemption{}

	keyCol := "`key`"
	if common.UsingPostgreSQL {
		keyCol = `"key"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		// Note: the previous implementation used tx.Set("gorm:query_option", "FOR UPDATE"),
		// which is the GORM v1 idiom. GORM v2 only stores that key in the statement
		// settings and emits no locking clause, so no row lock was ever taken: two
		// concurrent redemptions of the same key both observed status == enabled and both
		// credited the quota.
		// SQLite has no FOR UPDATE syntax, so the lock is skipped there; the
		// compare-and-swap below is what actually makes this safe on every backend.
		query := tx
		if !common.UsingSQLite {
			query = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		err := query.Where(keyCol+" = ?", key).First(redemption).Error
		if err != nil {
			return errors.New("无效的兑换码")
		}
		if redemption.Status != RedemptionCodeStatusEnabled {
			return errors.New("该兑换码已被使用")
		}
		// Compare-and-swap on status: only the transaction that actually flips
		// enabled -> used may credit the quota. This does not rely on SELECT ... FOR
		// UPDATE being supported, so it also holds on SQLite.
		result := tx.Model(&Redemption{}).
			Where("id = ? AND status = ?", redemption.Id, RedemptionCodeStatusEnabled).
			Updates(map[string]interface{}{
				"redeemed_time": helper.GetTimestamp(),
				"status":        RedemptionCodeStatusUsed,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("该兑换码已被使用")
		}
		redemption.RedeemedTime = helper.GetTimestamp()
		redemption.Status = RedemptionCodeStatusUsed
		return tx.Model(&User{}).Where("id = ?", userId).Update("quota", gorm.Expr("quota + ?", redemption.Quota)).Error
	})
	if err != nil {
		return 0, errors.New("兑换失败，" + err.Error())
	}
	RecordLog(ctx, userId, LogTypeTopup, fmt.Sprintf("通过兑换码充值 %s", common.LogQuota(redemption.Quota)))
	return redemption.Quota, nil
}

func (redemption *Redemption) Insert() error {
	var err error
	err = DB.Create(redemption).Error
	return err
}

func (redemption *Redemption) SelectUpdate() error {
	// This can update zero values
	return DB.Model(redemption).Select("redeemed_time", "status").Updates(redemption).Error
}

// Update Make sure your token's fields is completed, because this will update non-zero values
func (redemption *Redemption) Update() error {
	var err error
	err = DB.Model(redemption).Select("name", "status", "quota", "redeemed_time").Updates(redemption).Error
	return err
}

func (redemption *Redemption) Delete() error {
	var err error
	err = DB.Delete(redemption).Error
	return err
}

func DeleteRedemptionById(id int) (err error) {
	if id == 0 {
		return errors.New("id 为空！")
	}
	redemption := Redemption{Id: id}
	err = DB.Where(redemption).First(&redemption).Error
	if err != nil {
		return err
	}
	return redemption.Delete()
}
