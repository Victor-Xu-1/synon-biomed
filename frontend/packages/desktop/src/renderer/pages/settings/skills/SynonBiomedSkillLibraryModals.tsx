import { Button, Checkbox, Input, Message, Modal, Select, Spin } from '@arco-design/web-react';
import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { SkillFilePreview } from './SkillFilePreview';
import { skillSourceLabel } from './skillSourceLabel';
import { resolveSkillDescription } from '@/renderer/services/skills/synonBiomedSkillDescriptions';
import { getSynonBiomedSkillCategoryLabel } from '@/renderer/services/skills/synonBiomedSkillCategories';
import {
  deleteSynonBiomedSkillDraft,
  duplicateSynonBiomedSkill,
  importSynonBiomedSkillFile,
  loadSynonBiomedSkillFileContent,
  loadSynonBiomedSkillFiles,
  importSynonBiomedRepositorySkills,
  previewSynonBiomedSkillRepository,
  publishSynonBiomedSkillDraft,
  saveSynonBiomedSkillDraftFile,
  type SynonBiomedSkillRepoPreview,
} from '@/renderer/services/skills/synonBiomedSkillLibrary';

export type SkillModalItem = {
  name: string;
  displayName: string;
  description: string;
  description_i18n?: Record<string, string>;
  source: string;
  license?: string | null;
  category?: string | null;
  attachedAgents?: string[];
  thirdParty?: Array<{
    kind: string;
    name: string;
    provider?: string;
    license?: string;
    termsUrl?: string;
    infoUrl?: string;
  }>;
};

type CommonModalProps = {
  visible: boolean;
  onClose: () => void;
  onChanged: () => void | Promise<void>;
};

export function GitHubSkillImportModal({
  visible,
  initialRepo = '',
  onClose,
  onChanged,
}: CommonModalProps & { initialRepo?: string }) {
  const { t } = useTranslation();
  const [message, contextHolder] = Message.useMessage({ maxCount: 2 });
  const [repo, setRepo] = useState(initialRepo);
  const [preview, setPreview] = useState<SynonBiomedSkillRepoPreview | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [previewing, setPreviewing] = useState(false);
  const [importing, setImporting] = useState(false);

  useEffect(() => {
    if (!visible) return;
    setRepo(initialRepo);
    setPreview(null);
    setSelected([]);
  }, [initialRepo, visible]);

  const runPreview = async () => {
    const normalized = repo.trim();
    if (!normalized) return;
    setPreviewing(true);
    try {
      const next = await previewSynonBiomedSkillRepository(normalized);
      setPreview(next);
      setSelected(next.skills.filter((skill) => skill.selected).map((skill) => skill.name));
    } catch (error) {
      console.error('Failed to preview GitHub skill repository:', error);
      setPreview(null);
      setSelected([]);
      message.error(t('settings.skillsSettings.modals.github.previewFailed'));
    } finally {
      setPreviewing(false);
    }
  };

  const runImport = async () => {
    if (!preview || selected.length === 0) return;
    setImporting(true);
    try {
      const result = await importSynonBiomedRepositorySkills(preview, selected);
      if (result.skipped.length > 0) {
        message.warning(
          t('settings.skillsSettings.modals.github.importPartial', {
            imported: result.imported.length,
            skipped: result.skipped.length,
          })
        );
      } else {
        message.success(t('settings.skillsSettings.modals.github.imported', { count: result.imported.length }));
      }
      await onChanged();
      onClose();
    } catch (error) {
      console.error('Failed to import GitHub skills:', error);
      message.error(t('settings.skillsSettings.modals.github.importFailed'));
    } finally {
      setImporting(false);
    }
  };

  return (
    <Modal
      title={t('settings.skillsSettings.modals.github.title')}
      visible={visible}
      onCancel={onClose}
      autoFocus={false}
      focusLock
      className='max-w-[calc(100vw-24px)]'
      style={{ width: 680 }}
      footer={
        <div className='flex justify-end gap-8px'>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type='primary' loading={importing} disabled={!preview || selected.length === 0} onClick={runImport}>
            {t('settings.skillsSettings.modals.github.importSelected', { count: selected.length })}
          </Button>
        </div>
      }
    >
      {contextHolder}
      <div className='flex flex-col gap-14px' data-testid='github-skill-import-modal'>
        <div>
          <div className='mb-6px text-13px font-medium text-t-primary'>
            {t('settings.skillsSettings.modals.github.repository')}
          </div>
          <div className='flex gap-8px max-sm:flex-col'>
            <Input
              value={repo}
              onChange={setRepo}
              placeholder='https://github.com/org/repository'
              aria-label={t('settings.skillsSettings.modals.github.repository')}
              onPressEnter={() => void runPreview()}
            />
            <Button loading={previewing} disabled={!repo.trim()} onClick={() => void runPreview()}>
              {t('settings.skillsSettings.modals.github.readRepository')}
            </Button>
          </div>
          <div className='mt-6px text-12px text-t-tertiary'>{t('settings.skillsSettings.modals.github.hint')}</div>
        </div>

        {previewing ? (
          <div className='flex min-h-160px items-center justify-center'>
            <Spin />
          </div>
        ) : preview ? (
          <div className='min-h-0 overflow-hidden border border-arco-2 rd-6px'>
            <div className='border-b border-arco-2 bg-fill-1 px-12px py-10px'>
              <div className='truncate text-13px font-medium text-t-primary'>{preview.slug || preview.repo}</div>
              <div className='mt-2px truncate text-11px text-t-tertiary'>
                {preview.sha ? `@${preview.sha.slice(0, 12)}` : t('settings.skillsSettings.modals.github.missingSha')}
                {preview.license ? ` · ${preview.license}` : ''}
              </div>
            </div>
            <Checkbox.Group value={selected} onChange={(values) => setSelected(values.map(String))} className='w-full'>
              <div className='max-h-320px overflow-y-auto divide-y divide-[var(--color-border-2)]'>
                {preview.skills.map((skill) => (
                  <label key={skill.name} className='flex cursor-pointer items-start gap-10px px-12px py-10px'>
                    <Checkbox value={skill.name} className='mt-1px' />
                    <span className='min-w-0 flex-1'>
                      <span className='block truncate text-13px font-medium text-t-primary'>{skill.displayName}</span>
                      <span className='mt-2px block line-clamp-2 text-12px text-t-secondary'>{skill.description}</span>
                    </span>
                  </label>
                ))}
                {preview.skills.length === 0 ? (
                  <div className='px-12px py-30px text-center text-13px text-t-secondary'>
                    {t('settings.skillsSettings.modals.github.empty')}
                  </div>
                ) : null}
              </div>
            </Checkbox.Group>
          </div>
        ) : null}
      </div>
    </Modal>
  );
}

export function CreatePersonalSkillModal({
  visible,
  onClose,
  onChanged,
  initialDescription = '',
}: CommonModalProps & { initialDescription?: string }) {
  const { t } = useTranslation();
  const [message, contextHolder] = Message.useMessage({ maxCount: 2 });
  const [name, setName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [saving, setSaving] = useState(false);
  const normalizedName = normalizeSkillName(name);

  useEffect(() => {
    if (!visible) return;
    setName('');
    setDisplayName('');
    setDescription(initialDescription);
  }, [initialDescription, visible]);

  const create = async () => {
    if (!normalizedName || !displayName.trim() || !description.trim()) return;
    setSaving(true);
    try {
      const content = `---\nname: ${normalizedName}\ndescription: ${yamlScalar(description)}\nmetadata:\n  display-name: ${yamlScalar(displayName)}\n---\n\n# ${displayName.trim()}\n\n${description.trim()}\n`;
      await importSynonBiomedSkillFile(
        new File([content], `${normalizedName}.md`, { type: 'text/markdown' }),
        normalizedName
      );
      message.success(t('settings.skillsSettings.modals.create.created'));
      await onChanged();
      onClose();
    } catch (error) {
      console.error('Failed to create personal skill:', error);
      message.error(t('settings.skillsSettings.modals.create.createFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={t('settings.skillsSettings.modals.create.title')}
      visible={visible}
      onCancel={onClose}
      autoFocus={false}
      focusLock
      className='max-w-[calc(100vw-24px)]'
      style={{ width: 560 }}
      footer={
        <div className='flex justify-end gap-8px'>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            type='primary'
            loading={saving}
            disabled={!normalizedName || !displayName.trim() || !description.trim()}
            onClick={() => void create()}
          >
            {t('settings.skillsSettings.modals.create.createDraft')}
          </Button>
        </div>
      }
    >
      {contextHolder}
      <div className='flex flex-col gap-14px' data-testid='create-personal-skill-modal'>
        <Field
          label={t('settings.skillsSettings.modals.create.name')}
          hint={
            normalizedName && normalizedName !== name
              ? t('settings.skillsSettings.modals.create.normalizedHint', { name: normalizedName })
              : undefined
          }
        >
          <Input
            value={name}
            onChange={setName}
            placeholder={t('settings.skillsSettings.modals.create.namePlaceholder')}
            aria-label={t('settings.skillsSettings.modals.create.name')}
          />
        </Field>
        <Field label={t('settings.skillsSettings.modals.create.displayName')}>
          <Input
            value={displayName}
            onChange={setDisplayName}
            placeholder={t('settings.skillsSettings.modals.create.displayNamePlaceholder')}
            aria-label={t('settings.skillsSettings.modals.create.displayName')}
          />
        </Field>
        <Field label={t('settings.skillsSettings.modals.create.description')}>
          <Input.TextArea
            value={description}
            onChange={setDescription}
            autoSize={{ minRows: 3, maxRows: 6 }}
            placeholder={t('settings.skillsSettings.modals.create.descriptionPlaceholder')}
            aria-label={t('settings.skillsSettings.modals.create.description')}
          />
        </Field>
      </div>
    </Modal>
  );
}

export function SkillDetailModal({
  visible,
  skill,
  draft,
  editable,
  onClose,
  onChanged,
}: CommonModalProps & { skill: SkillModalItem | null; draft: boolean; editable: boolean }) {
  const { t, i18n } = useTranslation();
  const [message, contextHolder] = Message.useMessage({ maxCount: 2 });
  const messageRef = useRef(message);
  const translationRef = useRef(t);
  messageRef.current = message;
  translationRef.current = t;
  const generation = useRef(0);
  const [files, setFiles] = useState<string[]>([]);
  const [path, setPath] = useState('');
  const [content, setContent] = useState('');
  const [originalContent, setOriginalContent] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [copyVisible, setCopyVisible] = useState(false);
  const [copyName, setCopyName] = useState('');
  const [copying, setCopying] = useState(false);

  useEffect(() => {
    const current = ++generation.current;
    setFiles([]);
    setPath('');
    setContent('');
    setOriginalContent('');
    setCopyVisible(false);
    setCopyName(skill ? `${skill.name}-custom` : '');
    if (!visible || !skill) return;
    setLoading(true);
    void loadSynonBiomedSkillFiles(skill.name)
      .then(async (nextFiles) => {
        if (generation.current !== current) return;
        const firstPath = nextFiles.includes('SKILL.md') ? 'SKILL.md' : (nextFiles[0] ?? '');
        setFiles(nextFiles);
        setPath(firstPath);
        if (!firstPath) return;
        const nextContent = await loadSynonBiomedSkillFileContent(skill.name, firstPath);
        if (generation.current === current) {
          setContent(nextContent);
          setOriginalContent(nextContent);
        }
      })
      .catch((error) => {
        console.error('Failed to load skill files:', error);
        if (generation.current === current) {
          setPath('');
          setContent('');
          setOriginalContent('');
          messageRef.current.error(translationRef.current('settings.skillsSettings.modals.detail.fileLoadFailed'));
        }
      })
      .finally(() => {
        if (generation.current === current) setLoading(false);
      });
    return () => {
      generation.current += 1;
    };
  }, [skill?.name, visible]);

  const selectFile = async (nextPath: string) => {
    if (!skill) return;
    const current = ++generation.current;
    setPath(nextPath);
    setLoading(true);
    setContent('');
    setOriginalContent('');
    try {
      const nextContent = await loadSynonBiomedSkillFileContent(skill.name, nextPath);
      if (generation.current !== current) return;
      setContent(nextContent);
      setOriginalContent(nextContent);
    } catch (error) {
      if (generation.current !== current) return;
      console.error('Failed to load skill file content:', error);
      setContent('');
      setPath('');
      messageRef.current.error(translationRef.current('settings.skillsSettings.modals.detail.fileLoadFailed'));
    } finally {
      if (generation.current === current) setLoading(false);
    }
  };

  const save = async () => {
    if (!skill || !path) return;
    setSaving(true);
    try {
      await saveSynonBiomedSkillDraftFile(skill.name, path, originalContent, content);
      setOriginalContent(content);
      messageRef.current.success(translationRef.current('settings.skillsSettings.modals.detail.draftSaved'));
      await onChanged();
    } catch (error) {
      console.error('Failed to save skill draft:', error);
      messageRef.current.error(translationRef.current('settings.skillsSettings.modals.detail.draftSaveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const publish = async () => {
    if (!skill) return;
    setSaving(true);
    try {
      await publishSynonBiomedSkillDraft(skill.name, false);
      messageRef.current.success(translationRef.current('settings.skillsSettings.modals.detail.published'));
      await onChanged();
      onClose();
    } catch (error) {
      console.error('Failed to publish skill:', error);
      messageRef.current.error(translationRef.current('settings.skillsSettings.modals.detail.publishFailed'));
    } finally {
      setSaving(false);
    }
  };

  const removeDraft = async () => {
    if (!skill) return;
    setSaving(true);
    try {
      await deleteSynonBiomedSkillDraft(skill.name);
      messageRef.current.success(translationRef.current('settings.skillsSettings.modals.detail.draftDeleted'));
      await onChanged();
      onClose();
    } catch (error) {
      console.error('Failed to delete skill draft:', error);
      messageRef.current.error(translationRef.current('settings.skillsSettings.modals.detail.draftDeleteFailed'));
    } finally {
      setSaving(false);
    }
  };

  const duplicate = async () => {
    if (!skill) return;
    const normalized = copyName.trim();
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(normalized)) {
      messageRef.current.warning(translationRef.current('settings.skillsSettings.modals.detail.invalidCopyName'));
      return;
    }
    setCopying(true);
    try {
      await duplicateSynonBiomedSkill(skill.name, normalized);
      messageRef.current.success(translationRef.current('settings.skillsSettings.modals.detail.copyCreated'));
      setCopyVisible(false);
      await onChanged();
      onClose();
    } catch (error) {
      console.error('Failed to duplicate skill:', error);
      messageRef.current.error(translationRef.current('settings.skillsSettings.modals.detail.copyFailed'));
    } finally {
      setCopying(false);
    }
  };

  const footer = draft ? (
    <div className='flex flex-wrap justify-between gap-8px'>
      <Button status='danger' disabled={saving} onClick={() => void removeDraft()}>
        {t('settings.skillsSettings.modals.detail.deleteDraft')}
      </Button>
      <div className='flex gap-8px'>
        <Button onClick={onClose}>{t('common.close')}</Button>
        <Button loading={saving} disabled={loading || !path} onClick={() => void save()}>
          {t('common.save')}
        </Button>
        <Button type='primary' loading={saving} disabled={loading || !path} onClick={() => void publish()}>
          {t('settings.skillsSettings.modals.detail.publish')}
        </Button>
      </div>
    </div>
  ) : editable ? (
    <div className='flex justify-end gap-8px'>
      <Button onClick={onClose}>{t('common.close')}</Button>
      <Button
        type='primary'
        loading={saving}
        disabled={loading || !path || content === originalContent}
        onClick={() => void save()}
      >
        {t('common.save')}
      </Button>
    </div>
  ) : (
    <div className='flex justify-end gap-8px'>
      <Button onClick={onClose}>{t('common.close')}</Button>
      <Button type='primary' onClick={() => setCopyVisible(true)}>
        {t('settings.skillsSettings.modals.detail.createEditableCopy')}
      </Button>
    </div>
  );

  return (
    <>
      <Modal
        title={skill?.displayName || 'Skill'}
        visible={visible}
        onCancel={onClose}
        autoFocus={false}
        focusLock
        className='synon-biomed-skill-detail-modal max-w-[calc(100vw-24px)]'
        style={{ width: 860 }}
        footer={footer}
      >
        {contextHolder}
        <div className='flex min-h-420px flex-col gap-18px' data-testid='skill-detail-modal'>
          {skill ? (
            <header className='border-b border-arco-2 pb-16px'>
              <div className='flex min-w-0 flex-wrap items-center gap-8px'>
                <span className='break-words text-20px font-semibold text-t-primary'>{skill.displayName}</span>
              </div>
              <p className='mb-0 mt-8px text-13px leading-21px text-t-secondary'>
                {resolveSkillDescription(skill.name, skill.description, i18n.language, skill.description_i18n)}
              </p>
            </header>
          ) : null}

          <section className='min-h-0 flex-1'>
            <div className='mb-10px flex items-center justify-between gap-10px'>
              <h3 className='m-0 text-14px font-semibold text-t-primary'>
                {t('settings.skillsSettings.modals.detail.files')}
              </h3>
              {files.length > 1 ? (
                <Select
                  value={path}
                  onChange={(value) => void selectFile(value)}
                  aria-label={t('settings.skillsSettings.modals.detail.fileLabel')}
                  className='w-260px max-w-full'
                  size='small'
                >
                  {files.map((file) => (
                    <Select.Option key={file} value={file}>
                      {file}
                    </Select.Option>
                  ))}
                </Select>
              ) : files.length === 1 ? (
                <span className='font-mono text-12px text-t-tertiary'>{files[0]}</span>
              ) : null}
            </div>

            <div className='relative min-h-360px overflow-hidden border border-arco-2 rd-6px bg-2'>
              {loading ? (
                <div className='absolute inset-0 z-10 flex items-center justify-center bg-2/80'>
                  <Spin />
                </div>
              ) : null}
              {!loading && files.length === 0 ? (
                <div className='flex min-h-360px flex-col items-center justify-center px-24px text-center'>
                  <div className='text-13px font-medium text-t-primary'>
                    {t('settings.skillsSettings.modals.detail.fileUnavailableTitle')}
                  </div>
                  <div className='mt-6px max-w-460px text-12px leading-20px text-t-tertiary'>
                    {t('settings.skillsSettings.modals.detail.fileUnavailableBody')}
                  </div>
                </div>
              ) : editable ? (
                <Input.TextArea
                  value={content}
                  onChange={setContent}
                  aria-label={t('settings.skillsSettings.modals.detail.fileContentLabel')}
                  className='h-full min-h-360px !border-0 !font-mono !text-12px'
                />
              ) : (
                <div className='max-h-500px min-h-360px overflow-auto px-18px py-14px text-13px leading-21px'>
                  <SkillFilePreview content={content} path={path} />
                </div>
              )}
            </div>
          </section>

          {skill ? (
            <section className='border-t border-arco-2 pt-14px'>
              <h3 className='m-0 text-14px font-semibold text-t-primary'>
                {t('settings.skillsSettings.modals.detail.details')}
              </h3>
              <dl className='mt-10px grid grid-cols-[120px_minmax(0,1fr)] gap-x-16px gap-y-8px text-12px'>
                <dt className='text-t-tertiary'>{t('settings.skillsSettings.modals.detail.identifier')}</dt>
                <dd className='m-0 break-all font-mono text-t-primary'>{skill.name}</dd>
                <dt className='text-t-tertiary'>{t('settings.skillsSettings.sourceFilter')}</dt>
                <dd className='m-0 text-t-primary'>{skillSourceLabel(skill.source, t)}</dd>
                {skill.category ? (
                  <>
                    <dt className='text-t-tertiary'>{t('settings.skillsSettings.modals.detail.category')}</dt>
                    <dd className='m-0 text-t-primary'>
                      {getSynonBiomedSkillCategoryLabel(skill.category, i18n.language)}
                    </dd>
                  </>
                ) : null}
                {skill.license ? (
                  <>
                    <dt className='text-t-tertiary'>{t('settings.skillsSettings.modals.detail.license')}</dt>
                    <dd className='m-0 text-t-primary'>{skill.license}</dd>
                  </>
                ) : null}
                {skill.attachedAgents?.length ? (
                  <>
                    <dt className='text-t-tertiary'>{t('settings.skillsSettings.modals.detail.callableExperts')}</dt>
                    <dd className='m-0 text-t-primary'>
                      {skill.attachedAgents.join(t('settings.skillsSettings.modals.detail.listSeparator'))}
                    </dd>
                  </>
                ) : null}
              </dl>
              {skill.thirdParty?.length ? (
                <div className='mt-14px'>
                  <div className='mb-8px text-12px font-medium text-t-primary'>
                    {t('settings.skillsSettings.modals.detail.thirdParty')}
                  </div>
                  <div className='divide-y divide-[var(--color-border-2)] border-y border-arco-2'>
                    {skill.thirdParty.map((item) => (
                      <div key={`${item.kind}:${item.name}`} className='flex gap-12px py-9px text-12px'>
                        <span className='w-90px shrink-0 text-t-tertiary'>{thirdPartyKindLabel(item.kind, t)}</span>
                        <span className='min-w-0 flex-1 text-t-primary'>
                          {item.name}
                          {item.provider ? ` · ${item.provider}` : ''}
                          {item.license ? ` (${item.license})` : ''}
                        </span>
                        {item.termsUrl || item.infoUrl ? (
                          <a
                            href={item.termsUrl || item.infoUrl}
                            target='_blank'
                            rel='noreferrer'
                            className='shrink-0 text-link-6 hover:underline'
                          >
                            {item.termsUrl
                              ? t('settings.skillsSettings.modals.detail.terms')
                              : t('settings.skillsSettings.modals.detail.info')}
                          </a>
                        ) : null}
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}
            </section>
          ) : null}
        </div>
      </Modal>

      <Modal
        title={t('settings.skillsSettings.modals.detail.copyTitle')}
        visible={copyVisible}
        onCancel={() => setCopyVisible(false)}
        autoFocus={false}
        focusLock
        style={{ width: 480 }}
        footer={
          <div className='flex justify-end gap-8px'>
            <Button disabled={copying} onClick={() => setCopyVisible(false)}>
              {t('common.cancel')}
            </Button>
            <Button type='primary' loading={copying} onClick={() => void duplicate()}>
              {t('settings.skillsSettings.modals.detail.createCopy')}
            </Button>
          </div>
        }
      >
        <Field
          label={t('settings.skillsSettings.modals.detail.copyName')}
          hint={t('settings.skillsSettings.modals.detail.copyHint')}
        >
          <Input
            value={copyName}
            onChange={setCopyName}
            aria-label={t('settings.skillsSettings.modals.detail.copyAria')}
            placeholder='example-custom'
          />
        </Field>
      </Modal>
    </>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className='block'>
      <span className='mb-6px flex items-center justify-between gap-8px text-13px font-medium text-t-primary'>
        <span>{label}</span>
        {hint ? <span className='text-11px font-normal text-t-tertiary'>{hint}</span> : null}
      </span>
      {children}
    </label>
  );
}

function thirdPartyKindLabel(kind: string, t: ReturnType<typeof useTranslation>['t']): string {
  if (kind === 'weights') return t('settings.skillsSettings.modals.detail.thirdPartyWeights');
  if (kind === 'service') return t('settings.skillsSettings.modals.detail.thirdPartyService');
  return kind || t('settings.skillsSettings.modals.detail.thirdPartyContent');
}

function normalizeSkillName(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64);
}

function yamlScalar(value: string): string {
  return JSON.stringify(value.trim());
}
