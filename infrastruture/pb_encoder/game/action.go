package gamepb

import (
	"github.com/beka-birhanu/vinom-client/service/i"
	"github.com/google/uuid"
)

var _ i.Action = &Action{}

func actionFromInterface(a i.Action) *Action {
	return &Action{
		Id:        a.GetID().String(),
		Direction: a.GetDirection(),
		From:      cellPositionInterface(a.RetriveFrom()),
	}
}

// RetriveFrom implements game.Action.
func (x *Action) RetriveFrom() i.CellPosition {
	return x.From
}

// SetFrom implements game.Action.
func (x *Action) SetFrom(c i.CellPosition) {
	x.From = cellPositionInterface(c)
}

// SetDirection implements game.Action.
func (x *Action) SetDirection(s string) {
	x.Direction = s
}

// GetID implements game.Action.
func (x *Action) GetID() uuid.UUID {
	id, _ := uuid.Parse(x.Id)
	return id
}

// SetID implements game.Action.
func (x *Action) SetID(i uuid.UUID) {
	x.Id = i.String()
}
