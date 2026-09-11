// Package state 提供业务对象状态转换的通用校验，不把状态判断分散到各个 Handler。
package state

import (
	"errors"
	"fmt"
)

// ErrTransitionNotAllowed 供业务 Handler 识别并转换为统一的状态流转错误码。
var ErrTransitionNotAllowed = errors.New("state transition not allowed")

// TransitionRule 定义一个对象允许从哪些状态进入目标状态。
type TransitionRule map[string]map[string]struct{}

// NewRule 使用“来源状态 -> 目标状态列表”构造不可变的状态转换规则。
func NewRule(definition map[string][]string) TransitionRule {
	rule := make(TransitionRule, len(definition))
	for from, targets := range definition {
		allowed := make(map[string]struct{}, len(targets))
		for _, target := range targets {
			allowed[target] = struct{}{}
		}
		rule[from] = allowed
	}
	return rule
}

// ValidateTransition 校验当前状态是否允许进入目标状态；相同状态不属于一次有效流转。
func ValidateTransition(rule TransitionRule, from, to string) error {
	if from == "" || to == "" || from == to {
		return fmt.Errorf("%w: %s -> %s", ErrTransitionNotAllowed, from, to)
	}
	if _, ok := rule[from][to]; !ok {
		return fmt.Errorf("%w: %s -> %s", ErrTransitionNotAllowed, from, to)
	}
	return nil
}
