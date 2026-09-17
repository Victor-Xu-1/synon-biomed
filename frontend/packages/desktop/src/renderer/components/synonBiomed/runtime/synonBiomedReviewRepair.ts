import type { SynonBiomedVerificationCheck } from '@/renderer/services/synonBiomedAnnotations';

const MAX_FIELD_LENGTH = 2400;

const trimField = (value: string | null): string => {
  const normalized = value?.trim() ?? '';
  return normalized.length > MAX_FIELD_LENGTH ? `${normalized.slice(0, MAX_FIELD_LENGTH)}…` : normalized;
};

const verdictLabel = (verdict: SynonBiomedVerificationCheck['verdict'], chinese: boolean): string => {
  if (chinese) {
    if (verdict === 'fail') return '需要修改';
    if (verdict === 'warn') return '提示';
  } else {
    if (verdict === 'fail') return 'needs revision';
    if (verdict === 'warn') return 'warning';
  }
  return verdict;
};

export const buildSynonBiomedReviewRepairPrompt = (
  checks: SynonBiomedVerificationCheck[],
  locale = 'zh-CN'
): string => {
  const chinese = locale.toLowerCase().startsWith('zh');
  const findings = checks.filter((check) => check.verdict === 'fail' || check.verdict === 'warn');
  const rows = findings.map((check, index) => {
    const parts = [
      `${index + 1}. [${verdictLabel(check.verdict, chinese)}${check.severity ? ` / ${check.severity}` : ''}]`,
      trimField(check.claim),
    ];
    if (check.evidence) parts.push(`${chinese ? '依据' : 'Evidence'}: ${trimField(check.evidence)}`);
    if (check.rebuttal) parts.push(`${chinese ? '补充说明' : 'Note'}: ${trimField(check.rebuttal)}`);
    return parts.filter(Boolean).join('\n   ');
  });

  if (chinese) {
    return [
      '请根据下面这次审阅发现的问题，修复上一轮任务的结果并重新生成完整最终答案。',
      '只处理这些问题，不要输出审阅过程；保留已经确认正确的内容，并在最终结果中直接体现修复后的内容。',
      '',
      '审阅问题：',
      ...rows,
    ].join('\n');
  }

  return [
    'Please fix the previous task result using the review findings below and regenerate the complete final answer.',
    'Address only these findings, do not describe the review process, preserve confirmed-correct content, and show the corrected result directly.',
    '',
    'Review findings:',
    ...rows,
  ].join('\n');
};
