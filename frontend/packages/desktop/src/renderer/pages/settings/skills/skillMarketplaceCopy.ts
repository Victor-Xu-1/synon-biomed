export type SkillMarketplaceCopy = {
  zhName: string;
  zhDescription: string;
};

const SKILL_MARKETPLACE_COPY: Record<string, SkillMarketplaceCopy> = {
  'anthropics-skills::algorithmic-art': {
    zhName: '算法艺术',
    zhDescription: '使用 p5.js 和可复现随机数创建算法艺术，并探索交互式参数、流场与粒子系统。',
  },
  'anthropics-skills::brand-guidelines': {
    zhName: '品牌设计规范',
    zhDescription: '将 Anthropic 的官方品牌色彩与字体规范应用到需要统一视觉风格的产物中。',
  },
  'anthropics-skills::canvas-design': {
    zhName: '画布设计',
    zhDescription: '以设计理念创建 PNG 或 PDF 静态视觉作品，强调原创设计与版权合规。',
  },
  'anthropics-skills::claude-api': {
    zhName: 'Claude API 参考',
    zhDescription: '提供 Claude API 与 Anthropic SDK 的模型、参数、流式、工具调用、MCP、缓存和 Token 参考。',
  },
  'anthropics-skills::doc-coauthoring': {
    zhName: '文档协作',
    zhDescription: '通过结构化协作流程共同撰写文档、提案、技术规范和决策文档。',
  },
  'anthropics-skills::docx': {
    zhName: 'Word 文档处理',
    zhDescription: '创建、读取、编辑、转换、校验和预览 Word 文档与文档模板。',
  },
  'anthropics-skills::frontend-design': {
    zhName: '前端设计',
    zhDescription: '为前端界面提供视觉方向、排版与交互设计指导，避免模板化和默认样式。',
  },
  'anthropics-skills::internal-comms': {
    zhName: '内部沟通',
    zhDescription: '按团队约定撰写状态更新、公告、FAQ、事件报告和其他内部沟通材料。',
  },
  'anthropics-skills::mcp-builder': {
    zhName: 'MCP 服务构建',
    zhDescription: '使用 Python FastMCP 或 Node/TypeScript SDK 构建高质量 MCP 服务并集成外部 API。',
  },
  'anthropics-skills::pdf': {
    zhName: 'PDF 文档处理',
    zhDescription: '处理 PDF 的读取、提取、合并、拆分、旋转、OCR、表单填写和预览。',
  },
  'anthropics-skills::pptx': {
    zhName: 'PowerPoint 演示文稿',
    zhDescription: '创建和编辑 PPTX/POTX 演示文稿、模板、讲稿，并处理合并、拆分和版式。',
  },
  'anthropics-skills::skill-creator': {
    zhName: 'Skill 创建与评估',
    zhDescription: '创建、修改、评估和优化 Skill，并提高触发准确率与可复用性。',
  },
  'anthropics-skills::slack-gif-creator': {
    zhName: 'Slack GIF 制作',
    zhDescription: '制作适合 Slack 的动画 GIF，涵盖动画概念、视觉设计和输出约束。',
  },
  'anthropics-skills::template-skill': {
    zhName: 'Skill 模板',
    zhDescription: '用于创建新 Skill 的基础模板，替换占位说明后即可扩展为具体能力。',
  },
  'anthropics-skills::theme-factory': {
    zhName: '主题工厂',
    zhDescription: '使用预设主题或动态主题，为幻灯片、文档、报告和 HTML 产物统一样式。',
  },
  'anthropics-skills::web-artifacts-builder': {
    zhName: 'Web 产物构建',
    zhDescription: '构建包含状态管理、路由和组件库的复杂 HTML/React Web 产物。',
  },
  'anthropics-skills::webapp-testing': {
    zhName: 'Web 应用测试',
    zhDescription: '使用 Playwright 交互并测试本地 Web 应用，支持验收、调试和截图。',
  },
  'anthropics-skills::xlsx': {
    zhName: '电子表格处理',
    zhDescription: '创建、读取、编辑、分析、校验和交付 XLSX、CSV 等电子表格文件。',
  },
  'bioconductor-ai-agent-skills::adr-author': {
    zhName: '架构决策记录',
    zhDescription: '按照 Nygard 格式和项目既有约定编写可审阅的架构决策记录。',
  },
  'bioconductor-ai-agent-skills::analyze-r-package': {
    zhName: 'R/Bioconductor 包分析',
    zhDescription: '分析 R 或 Bioconductor 包的用途、导出函数、结构和关键特征。',
  },
  'bioconductor-ai-agent-skills::bioc-howto': {
    zhName: 'Bioconductor 分析指南',
    zhDescription: '汇总基因组学、测序和组学数据分析相关的 Bioconductor 操作指南。',
  },
  'bioconductor-ai-agent-skills::bioc-pkg-finder': {
    zhName: 'Bioconductor 包检索',
    zhDescription: '根据研究任务或分析工作流寻找最合适的 R/Bioconductor 包。',
  },
  'bioconductor-ai-agent-skills::check-bioconductor-skills': {
    zhName: 'Bioconductor Skill 检查',
    zhDescription: '检查 Bioconductor AI Agent Skills 是否可用，并帮助选择合适的技能。',
  },
  'bioconductor-ai-agent-skills::create-package-instructions': {
    zhName: '创建包使用说明',
    zhDescription: '为 R 或 Bioconductor 包创建完整的 AI 使用说明，并写入 .github/instructions/。',
  },
  'bioconductor-ai-agent-skills::create-skill': {
    zhName: '创建 Skill',
    zhDescription: '通过协作问答流程创建新的 AI Agent Skill。',
  },
  'bioconductor-ai-agent-skills::document-skill': {
    zhName: 'Skill 文档维护',
    zhDescription: '创建或修改 Skill 后，自动更新 SKILLS.md 文档索引。',
  },
  'bioconductor-ai-agent-skills::improve-code-coverage': {
    zhName: '提升代码覆盖率',
    zhDescription: '使用 covr 分析 R 包覆盖率，识别测试缺口并补充测试用例。',
  },
  'bioconductor-ai-agent-skills::security-audit-r-package': {
    zhName: 'R 包安全审计',
    zhDescription: '对 R 或 Bioconductor 包执行全面的安全审计并整理风险证据。',
  },
  'bioconductor-ai-agent-skills::update-package-instructions': {
    zhName: '更新包使用说明',
    zhDescription: '当 R 或 Bioconductor 包发生变化时，更新现有的 AI 使用说明。',
  },
  'bioconductor-ai-agent-skills::update-r-news': {
    zhName: '更新 R NEWS',
    zhDescription: '根据 Git 提交历史起草或更新 R 包 NEWS 文件，必要时检查代码差异。',
  },
  'bioconductor-ai-agent-skills::validate-skill': {
    zhName: 'Skill 规范校验',
    zhDescription: '校验 Skill 是否符合 ai-agent-skills 仓库的格式和质量规范。',
  },
};

export function getSkillMarketplaceCopy(
  sourceId: string,
  skillName: string,
  englishDescription?: string | null
): SkillMarketplaceCopy {
  return (
    SKILL_MARKETPLACE_COPY[`${sourceId}::${skillName}`] ?? {
      zhName: skillName,
      zhDescription: englishDescription
        ? `该 Skill 提供可复用的工作流能力；英文原始说明见下方。`
        : '该 Skill 提供可复用的工作流能力。',
    }
  );
}

export const SKILL_MARKETPLACE_COPY_COUNT = Object.keys(SKILL_MARKETPLACE_COPY).length;
