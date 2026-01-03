package control

import (
	"fmt"
	"sync"
)

// PolicyRule 策略规则（ACL规则）
type PolicyRule struct {
	SenderID   string // 发送方 ID（支持 "*" 通配符）
	ReceiverID string // 接收方 ID（支持 "*" 通配符）
	Allowed    bool   // 是否允许
	Priority   int    // 优先级（数字越大优先级越高，用于规则匹配顺序）
}

// PolicyEngine 权限策略引擎
type PolicyEngine struct {
	mu      sync.RWMutex
	rules   map[string]*PolicyRule // key: "senderID:receiverID"
	defaultAllow bool                // 默认策略：true=允许所有, false=拒绝所有
}

// NewPolicyEngine 创建新的策略引擎
func NewPolicyEngine(defaultAllow bool) *PolicyEngine {
	return &PolicyEngine{
		rules:        make(map[string]*PolicyRule),
		defaultAllow: defaultAllow,
	}
}

// AddRule 添加策略规则
func (pe *PolicyEngine) AddRule(rule *PolicyRule) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	key := fmt.Sprintf("%s:%s", rule.SenderID, rule.ReceiverID)
	pe.rules[key] = rule
}

// RemoveRule 移除策略规则
func (pe *PolicyEngine) RemoveRule(senderID, receiverID string) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	key := fmt.Sprintf("%s:%s", senderID, receiverID)
	delete(pe.rules, key)
}

// CheckPermission 检查权限（ACL匹配）
func (pe *PolicyEngine) CheckPermission(senderID, receiverID string) (bool, string) {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	// 收集所有匹配的规则（按优先级排序）
	var matchedRules []*PolicyRule
	
	for _, rule := range pe.rules {
		senderMatch := rule.SenderID == "*" || rule.SenderID == senderID
		receiverMatch := rule.ReceiverID == "*" || rule.ReceiverID == receiverID
		
		if senderMatch && receiverMatch {
			matchedRules = append(matchedRules, rule)
		}
	}
	
	// 按优先级排序（优先级高的在前）
	if len(matchedRules) > 0 {
		// 简单排序：优先级高的优先匹配
		// 找到最高优先级的规则
		var highestRule *PolicyRule
		highestPriority := -1
		for _, rule := range matchedRules {
			if rule.Priority > highestPriority {
				highestPriority = rule.Priority
				highestRule = rule
			}
		}
		
		if highestRule != nil {
			if !highestRule.Allowed {
				return false, fmt.Sprintf("ACL rule denies %s -> %s (priority: %d)", senderID, receiverID, highestRule.Priority)
			}
			return true, fmt.Sprintf("allowed by ACL rule (priority: %d)", highestRule.Priority)
		}
	}

	// 使用默认策略
	if pe.defaultAllow {
		return true, "allowed by default policy"
	}

	return false, "denied by default policy"
}

// ListRules 列出所有规则
func (pe *PolicyEngine) ListRules() []*PolicyRule {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	rules := make([]*PolicyRule, 0, len(pe.rules))
	for _, rule := range pe.rules {
		rules = append(rules, rule)
	}

	return rules
}

// LoadDefaultRules 加载默认规则（示例）
// 为了支持多对多频道，需要配置所有参与者之间的双向权限
func (pe *PolicyEngine) LoadDefaultRules() {
	// connector-A 和 connector-B 之间的双向权限
	pe.AddRule(&PolicyRule{
		SenderID:   "connector-A",
		ReceiverID: "connector-B",
		Allowed:    true,
		Priority:    100,
	})
	pe.AddRule(&PolicyRule{
		SenderID:   "connector-B",
		ReceiverID: "connector-A",
		Allowed:    true,
		Priority:    100,
	})

	// connector-A 和 connector-C 之间的双向权限
	pe.AddRule(&PolicyRule{
		SenderID:   "connector-A",
		ReceiverID: "connector-C",
		Allowed:    true,
		Priority:    100,
	})
	pe.AddRule(&PolicyRule{
		SenderID:   "connector-C",
		ReceiverID: "connector-A",
		Allowed:    true,
		Priority:    100,
	})

	// connector-B 和 connector-C 之间的双向权限
	pe.AddRule(&PolicyRule{
		SenderID:   "connector-B",
		ReceiverID: "connector-C",
		Allowed:    true,
		Priority:    100,
	})
	pe.AddRule(&PolicyRule{
		SenderID:   "connector-C",
		ReceiverID: "connector-B",
		Allowed:    true,
		Priority:    100,
	})
}

// SetDefaultPolicy 设置默认策略
func (pe *PolicyEngine) SetDefaultPolicy(allow bool) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.defaultAllow = allow
}

// ValidateRule 验证规则的合法性
func (pe *PolicyEngine) ValidateRule(rule *PolicyRule) error {
	if rule.SenderID == "" {
		return fmt.Errorf("sender ID cannot be empty")
	}
	if rule.ReceiverID == "" {
		return fmt.Errorf("receiver ID cannot be empty")
	}
	return nil
}

