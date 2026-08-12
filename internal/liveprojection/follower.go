package liveprojection

import "github.com/PaulOctopusZLWB/dota2-ob/internal/session"

var ErrOutOfOrderHighWater = session.ErrOutOfOrderHighWater

type Projection = session.LiveProjection
type Health = session.LiveProjectionHealth
type Follower = session.LiveFollower

var New = session.NewLiveFollower
