package teacherimages

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/netip"
	"regexp"
	"strconv"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

// Budget atomically accounts for each signature across all serving instances.
type Budget interface {
	PrepareTeacherImage(context.Context, c.TeacherImageKey) error
	TakeTeacherImageSignature(context.Context, string, netip.Addr) error
}

type Delivery struct {
	host, key string
	budget    Budget
	now       func() time.Time
}

var hostName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$`)
var signingKey = regexp.MustCompile(`^[A-Za-z0-9]{6,40}$`)

func NewDelivery(host, key string, budget Budget) (*Delivery, error) {
	if !hostName.MatchString(host) || !signingKey.MatchString(key) || budget == nil {
		return nil, c.ErrInput
	}
	return &Delivery{host: host, key: key, budget: budget, now: time.Now}, nil
}

// Issue uses EdgeOne Type D with decimal issuance time. EdgeOne enforces the 60-second TTL.
func (d *Delivery) Issue(ctx context.Context, user string, ip netip.Addr, k c.TeacherImageKey) (string, error) {
	name, _, err := object(k)
	if err != nil || user == "" || !ip.IsValid() {
		return "", c.ErrInput
	}
	if err = d.budget.TakeTeacherImageSignature(ctx, user, ip.Unmap()); err != nil {
		return "", err
	}
	if err = d.budget.PrepareTeacherImage(ctx, k); err != nil {
		return "", err
	}
	path := "/" + name
	t := strconv.FormatInt(d.now().Unix(), 10)
	token := md5.Sum([]byte(d.key + path + t))
	return "https://" + d.host + path + "?token=" + hex.EncodeToString(token[:]) + "&t=" + t, nil
}
