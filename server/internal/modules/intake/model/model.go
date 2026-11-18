// Package model 定义客服澄清领域的提交人身份、澄清会话、对话消息、截图附件及低保真原型版本数据表模型（5.3、5.5 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(
		&Reporter{},
		&Conversation{},
		&ConversationMessage{},
		&Attachment{},
		&Prototype{},
	)
}

// Reporter 表示外部接入方提交反馈的用户档案，以（IntegrationID + ExternalUID）复合唯一识别（5.5 节）。
type Reporter struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:提交人主键ID" json:"id"`
	ProductID     uint64    `gorm:"index;not null;default:0;comment:最近使用产品ID" json:"productId"`
	IntegrationID uint64    `gorm:"uniqueIndex:uk_int_ext;not null;comment:所属接入方ID" json:"integrationId"`
	ExternalUID   string    `gorm:"uniqueIndex:uk_int_ext;size:128;not null;comment:接入方系统稳定外部用户ID" json:"externalUid"`
	DisplayName   string    `gorm:"size:128;not null;default:'';comment:提交人姓名或昵称" json:"displayName"`
	AttributesJSON string   `gorm:"type:text;comment:可选扩展属性JSON（角色、组织等）" json:"attributesJson"`
	CreatedAt     time.Time `gorm:"not null;comment:首次登记时间" json:"createdAt"`
	UpdatedAt     time.Time `gorm:"not null;comment:资料更新时间" json:"updatedAt"`
}

// TableName 返回提交人物理表名。
func (Reporter) TableName() string { return "reporters" }

// TableComment 返回提交人物理表中文注释。
func (Reporter) TableComment() string { return "外部业务系统反馈提交人身份档案表" }

// Conversation 表示一次客服 AI 需求澄清会话，支持在线对话模式与模型离线时的结构化表单留言模式（待 AI 跟进）。
type Conversation struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement;comment:澄清会话主键ID" json:"id"`
	ProductID         uint64    `gorm:"index;not null;comment:目标产品ID" json:"productId"`
	IntegrationID     uint64    `gorm:"index;not null;comment:来源接入方ID" json:"integrationId"`
	ReporterID        uint64    `gorm:"index;not null;comment:提交人主键ID" json:"reporterId"`
	ExternalUID       string    `gorm:"size:128;not null;index;comment:外部用户ID冗余" json:"externalUid"`
	Mode              string    `gorm:"size:24;not null;default:'chat';comment:会话模式（chat实时对话/form离线表单留言）" json:"mode"`
	IssueType         string    `gorm:"size:32;not null;default:'feature';comment:当前识别的需求类型（bug/feature/question）" json:"issueType"`
	Status            string    `gorm:"size:32;not null;default:'clarifying';index;comment:会话状态（clarifying澄清中/pending_ai待AI跟进/awaiting_confirm待确认需求卡/submitted已提交/answered咨询已解答）" json:"status"`
	EnvInfoJSON       string    `gorm:"type:text;comment:接入方附带的环境上下文JSON" json:"envInfoJson"`
	DraftJSON         string    `gorm:"type:mediumtext;comment:当前结构化需求草稿字段JSON" json:"draftJson"`
	ProposedCardJSON  string    `gorm:"type:mediumtext;comment:AI输出待提交人确认的需求卡或复现卡JSON" json:"proposedCardJson"`
	ConfirmedByUser   bool      `gorm:"not null;default:false;comment:提交人是否已明确点击确认需求卡" json:"confirmedByUser"`
	LatestPrototypeID uint64    `gorm:"not null;default:0;comment:最新生成的HTML原型ID" json:"latestPrototypeId"`
	RequirementID     uint64    `gorm:"index;not null;default:0;comment:正式提交后生成的需求ID" json:"requirementId"`
	CreatedAt         time.Time `gorm:"index;not null;comment:会话创建时间" json:"createdAt"`
	UpdatedAt         time.Time `gorm:"not null;comment:会话更新时间" json:"updatedAt"`
}

// TableName 返回澄清会话物理表名。
func (Conversation) TableName() string { return "conversations" }

// TableComment 返回澄清会话物理表中文注释。
func (Conversation) TableComment() string { return "客服AI需求澄清会话与草稿状态表" }

// ConversationMessage 存储澄清会话中的用户发言、客服 AI 回复、选项提问及工具调用记录。
type ConversationMessage struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement;comment:消息主键ID" json:"id"`
	ProductID       uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	ConversationID  uint64    `gorm:"index;not null;comment:所属澄清会话ID" json:"conversationId"`
	Role            string    `gorm:"size:24;not null;comment:发言角色（user/assistant/tool）" json:"role"`
	Content         string    `gorm:"type:mediumtext;comment:消息文本内容" json:"content"`
	AskOptionsJSON  string    `gorm:"type:text;comment:ask_user结构化单问题及最多4个选项JSON" json:"askOptionsJson,omitempty"`
	AttachmentsJSON string    `gorm:"type:text;comment:本条消息关联的截图附件ID列表JSON" json:"attachmentsJson,omitempty"`
	CreatedAt       time.Time `gorm:"index;not null;comment:发送时间" json:"createdAt"`
}

// TableName 返回会话消息物理表名。
func (ConversationMessage) TableName() string { return "conversation_messages" }

// TableComment 返回会话消息物理表中文注释。
func (ConversationMessage) TableComment() string { return "客服澄清会话消息明细表" }

// Attachment 存储提交人上传的截图附件元数据（限制 png/jpg/webp 且大小 <= 10 MB）。
type Attachment struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement;comment:附件主键ID" json:"id"`
	ProductID      uint64    `gorm:"index;not null;default:0;comment:关联产品ID" json:"productId"`
	IntegrationID  uint64    `gorm:"index;not null;comment:上传接入方ID" json:"integrationId"`
	ConversationID uint64    `gorm:"index;not null;default:0;comment:关联澄清会话ID" json:"conversationId"`
	FileName       string    `gorm:"size:255;not null;comment:原始安全文件名" json:"fileName"`
	MimeType       string    `gorm:"size:64;not null;comment:验证通过的图片MIME类型（image/png等）" json:"mimeType"`
	SizeBytes      int64     `gorm:"not null;comment:文件字节大小" json:"sizeBytes"`
	SHA256         string    `gorm:"size:64;not null;comment:文件SHA256哈希" json:"sha256"`
	StoragePath    string    `gorm:"size:512;not null;comment:服务端本地磁盘相对存储路径" json:"-"`
	CreatedAt      time.Time `gorm:"not null;comment:上传时间" json:"createdAt"`
}

// TableName 返回附件物理表名。
func (Attachment) TableName() string { return "attachments" }

// TableComment 返回附件物理表中文注释。
func (Attachment) TableComment() string { return "反馈澄清截图附件存储元数据表" }

// Prototype 保存在澄清过程中为新功能/改进类需求迭代生成的单文件无外链低保真 HTML 原型版本（5.3 节）。
type Prototype struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement;comment:原型主键ID" json:"id"`
	ProductID      uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	ConversationID uint64    `gorm:"index;not null;comment:所属澄清会话ID" json:"conversationId"`
	VersionNum     int       `gorm:"not null;default:1;comment:会话内递增原型版本号" json:"versionNum"`
	Title          string    `gorm:"size:128;not null;default:'';comment:原型界面说明标题" json:"title"`
	HTMLContent    string    `gorm:"type:mediumtext;comment:不含任何外部资源引用的独立单文件HTML源码" json:"htmlContent"`
	CreatedAt      time.Time `gorm:"not null;comment:生成时间" json:"createdAt"`
}

// TableName 返回原型版本物理表名。
func (Prototype) TableName() string { return "prototypes" }

// TableComment 返回原型版本物理表中文注释。
func (Prototype) TableComment() string { return "客服AI生成的单文件HTML低保真界面原型版本表" }
