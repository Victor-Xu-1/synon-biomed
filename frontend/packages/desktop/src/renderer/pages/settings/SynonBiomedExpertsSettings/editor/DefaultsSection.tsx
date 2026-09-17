import type { IMcpServer } from '@/common/config/storage';
import { Button, Select, Tooltip } from '@arco-design/web-react';
import { Brain, Lightning, LinkCloud, Shield, Toolkit } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { ConfigRow, ReadonlySelectionField, SectionCard } from './editorSectionPrimitives';
import styles from './DefaultsSection.module.css';

type SelectOption = { key: string; value: string; label: string; description?: string };
type EditableSkillOption = { value: string; label: string; isAuto?: boolean; disabled?: boolean };

const getEditorSelectPopupContainer = (node: HTMLElement) =>
  node.closest('[data-editor-popup-root]') ?? node.parentElement ?? document.body;

const AUTO_SELECT_VALUE = '__AUTO__';

const renderSummaryTag = ({ label }: { label: React.ReactNode }) => (
  <span className={styles.summaryTagText}>{label}</span>
);

type DefaultsSectionProps = {
  localeKey: string;
  isReadOnlyAssistant: boolean;
  isCreating: boolean;
  showSkills: boolean;
  defaultModelMode: 'auto' | 'fixed';
  setDefaultModelMode: (value: 'auto' | 'fixed') => void;
  defaultModelValue: string;
  setDefaultModelValue: (value: string) => void;
  defaultPermissionMode: 'auto' | 'fixed';
  setDefaultPermissionMode: (value: 'auto' | 'fixed') => void;
  defaultPermissionValue: string;
  setDefaultPermissionValue: (value: string) => void;
  defaultThoughtLevelMode: 'auto' | 'fixed';
  setDefaultThoughtLevelMode: (value: 'auto' | 'fixed') => void;
  defaultThoughtLevelValue: string;
  setDefaultThoughtLevelValue: (value: string) => void;
  defaultSkillsMode: 'auto' | 'fixed';
  setDefaultSkillsMode: (value: 'auto' | 'fixed') => void;
  defaultMcpMode: 'auto' | 'fixed';
  setDefaultMcpMode: (value: 'auto' | 'fixed') => void;
  modelOptions: SelectOption[];
  permissionOptions: Array<{ value: string; label: string; description?: string }>;
  showThoughtLevelDefault: boolean;
  thoughtLevelOptions: Array<{ value: string; label: string; description?: string }>;
  editableSkillOptions: EditableSkillOption[];
  selectedSkillValues: string[];
  enabledMcpServers: IMcpServer[];
  selectedMcpIds: string[];
  setSelectedMcpIds: (value: string[]) => void;
  handleSkillSelectionChange: (values: string[]) => void;
  selectedItemsLabel: (count: number) => string;
  autoDefaultOptionLabel: string;
  readonlySelectionSummary: (items: string[], emptyLabel: string) => string;
};

const DefaultsSection: React.FC<DefaultsSectionProps> = ({
  localeKey,
  isReadOnlyAssistant,
  showSkills,
  defaultModelMode,
  setDefaultModelMode,
  defaultModelValue,
  setDefaultModelValue,
  defaultPermissionMode,
  setDefaultPermissionMode,
  defaultPermissionValue,
  setDefaultPermissionValue,
  defaultThoughtLevelMode,
  setDefaultThoughtLevelMode,
  defaultThoughtLevelValue,
  setDefaultThoughtLevelValue,
  defaultSkillsMode,
  setDefaultSkillsMode,
  defaultMcpMode,
  setDefaultMcpMode,
  modelOptions,
  permissionOptions,
  showThoughtLevelDefault,
  thoughtLevelOptions,
  editableSkillOptions,
  selectedSkillValues,
  enabledMcpServers,
  selectedMcpIds,
  setSelectedMcpIds,
  handleSkillSelectionChange,
  selectedItemsLabel,
  autoDefaultOptionLabel,
  readonlySelectionSummary,
}) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const canEditDefaultModelAndPermission = !isReadOnlyAssistant;
  const canEditDefaultSkillsAndMcps = !isReadOnlyAssistant;
  const permissionLabel = (value: string, fallback: string) => {
    const key = `agentMode.${value}`;
    const translated = t(key);
    return translated === key ? fallback : translated;
  };
  const hasFixedThoughtLevelValue =
    defaultThoughtLevelMode === 'fixed' &&
    defaultThoughtLevelValue &&
    thoughtLevelOptions.some((option) => option.value === defaultThoughtLevelValue);

  return (
    <SectionCard
      title={t('settings.assistantDefaultConfigSection')}
      legend={{
        label: t('settings.assistantOnlyNewConversation'),
        tone: 'next',
      }}
      testId='assistant-card-defaults'
    >
      <div className='space-y-16px'>
        <ConfigRow
          icon={<LinkCloud theme='outline' size='14' />}
          label={t('settings.assistantDefaultModelLabel')}
          hint={t('settings.assistantDefaultConfigHint')}
        >
          <Select
            key={`assistant-default-model-${localeKey}`}
            getPopupContainer={getEditorSelectPopupContainer}
            value={defaultModelMode === 'fixed' && defaultModelValue ? defaultModelValue : AUTO_SELECT_VALUE}
            onChange={(value) => {
              const nextValue = value as string;
              if (nextValue === AUTO_SELECT_VALUE) {
                setDefaultModelMode('auto');
                setDefaultModelValue('');
                return;
              }
              setDefaultModelMode('fixed');
              setDefaultModelValue(nextValue);
            }}
            disabled={!canEditDefaultModelAndPermission}
            allowClear={false}
            placeholder={t('settings.assistantSelectDefaultModel')}
            notFoundContent={t('settings.assistantNoAvailableModels')}
            data-testid='select-assistant-default-model'
          >
            <Select.Option value={AUTO_SELECT_VALUE}>{autoDefaultOptionLabel}</Select.Option>
            {modelOptions.map((option) => (
              <Select.Option key={`${localeKey}-${option.key}`} value={option.value}>
                {option.description ? (
                  <Tooltip content={option.description} position='right'>
                    <span className='block min-w-0 truncate'>{option.label}</span>
                  </Tooltip>
                ) : (
                  <span className='block min-w-0 truncate'>{option.label}</span>
                )}
              </Select.Option>
            ))}
          </Select>
        </ConfigRow>

        <ConfigRow
          icon={<Shield theme='outline' size='14' />}
          label={t('settings.assistantDefaultPermissionLabel')}
          hint={t('settings.assistantDefaultPermissionHint')}
        >
          <Select
            key={`assistant-default-permission-${localeKey}-${defaultPermissionMode}`}
            getPopupContainer={getEditorSelectPopupContainer}
            value={
              defaultPermissionMode === 'fixed' && defaultPermissionValue ? defaultPermissionValue : AUTO_SELECT_VALUE
            }
            onChange={(value) => {
              const nextValue = value as string;
              if (nextValue === AUTO_SELECT_VALUE) {
                setDefaultPermissionMode('auto');
                setDefaultPermissionValue('');
                return;
              }
              setDefaultPermissionMode('fixed');
              setDefaultPermissionValue(nextValue);
            }}
            disabled={!canEditDefaultModelAndPermission}
            allowClear={false}
            placeholder={t('settings.assistantSelectDefaultPermission')}
            notFoundContent={t('settings.assistantNoPermissionModes')}
            data-testid='select-assistant-default-permission'
          >
            <Select.Option value={AUTO_SELECT_VALUE}>{autoDefaultOptionLabel}</Select.Option>
            {permissionOptions.map((option) => (
              <Select.Option key={`${localeKey}-${option.value}`} value={option.value}>
                {option.description ? (
                  <Tooltip content={option.description} position='right'>
                    <span className='block min-w-0 truncate'>{permissionLabel(option.value, option.label)}</span>
                  </Tooltip>
                ) : (
                  <span className='block min-w-0 truncate'>{permissionLabel(option.value, option.label)}</span>
                )}
              </Select.Option>
            ))}
          </Select>
        </ConfigRow>

        {showThoughtLevelDefault ? (
          <ConfigRow icon={<Brain theme='outline' size='14' />} label={t('settings.assistantDefaultThoughtLevelLabel')}>
            <Select
              key={`assistant-default-thought-level-${localeKey}-${defaultThoughtLevelMode}`}
              getPopupContainer={getEditorSelectPopupContainer}
              value={hasFixedThoughtLevelValue ? defaultThoughtLevelValue : AUTO_SELECT_VALUE}
              onChange={(value) => {
                const nextValue = value as string;
                if (nextValue === AUTO_SELECT_VALUE) {
                  setDefaultThoughtLevelMode('auto');
                  setDefaultThoughtLevelValue('');
                  return;
                }
                setDefaultThoughtLevelMode('fixed');
                setDefaultThoughtLevelValue(nextValue);
              }}
              disabled={!canEditDefaultModelAndPermission}
              allowClear={false}
              placeholder={t('settings.assistantSelectDefaultThoughtLevel')}
              data-testid='select-assistant-default-thought-level'
            >
              <Select.Option value={AUTO_SELECT_VALUE}>{autoDefaultOptionLabel}</Select.Option>
              {thoughtLevelOptions.map((option) => (
                <Select.Option key={`${localeKey}-${option.value}`} value={option.value}>
                  {option.description ? (
                    <Tooltip content={option.description} position='right'>
                      <span className='block min-w-0 truncate'>{option.label}</span>
                    </Tooltip>
                  ) : (
                    <span className='block min-w-0 truncate'>{option.label}</span>
                  )}
                </Select.Option>
              ))}
            </Select>
          </ConfigRow>
        ) : null}

        {showSkills ? (
          <ConfigRow
            icon={<Lightning theme='outline' size='14' />}
            label={t('settings.assistantDefaultSkillsLabel')}
            hint={
              <Button
                type='text'
                size='mini'
                onClick={() => navigate('/settings/skills')}
                data-testid='btn-open-skills-settings'
                className='!h-auto !px-0 !text-primary-6'
              >
                {t('settings.synonBiomedSkillsManage')}
              </Button>
            }
          >
            {canEditDefaultSkillsAndMcps ? (
              <Select
                className={styles.summarySelect}
                getPopupContainer={getEditorSelectPopupContainer}
                mode='multiple'
                value={defaultSkillsMode === 'fixed' ? selectedSkillValues : [AUTO_SELECT_VALUE]}
                onChange={(value) => {
                  const nextValues = ((value as string[]) ?? []).filter(Boolean);
                  if (nextValues.length === 0) {
                    setDefaultSkillsMode('fixed');
                    handleSkillSelectionChange([]);
                    return;
                  }

                  const filteredValues = nextValues.filter((item) => item !== AUTO_SELECT_VALUE);
                  if (filteredValues.length === 0) {
                    setDefaultSkillsMode('auto');
                    handleSkillSelectionChange([]);
                    return;
                  }

                  setDefaultSkillsMode('fixed');
                  handleSkillSelectionChange(filteredValues);
                }}
                onClear={() => setDefaultSkillsMode('auto')}
                allowClear
                maxTagCount={{
                  count: 0,
                  render: () =>
                    defaultSkillsMode === 'auto'
                      ? autoDefaultOptionLabel
                      : selectedItemsLabel(selectedSkillValues.length),
                }}
                placeholder={
                  defaultSkillsMode === 'auto' ? autoDefaultOptionLabel : t('settings.assistantDefaultSkillsLabel')
                }
                data-testid='select-assistant-default-skills'
                dropdownRender={(menu) => (
                  <div>
                    <Button
                      type='text'
                      size='small'
                      className='!mx-8px !mt-8px !justify-start !px-8px !text-primary-6'
                      onClick={(event) => {
                        event.preventDefault();
                        event.stopPropagation();
                        setDefaultSkillsMode('auto');
                        handleSkillSelectionChange([]);
                      }}
                    >
                      {autoDefaultOptionLabel}
                    </Button>
                    {menu}
                  </div>
                )}
                renderFormat={() =>
                  defaultSkillsMode === 'auto'
                    ? autoDefaultOptionLabel
                    : readonlySelectionSummary(selectedSkillValues, t('settings.assistantNoDefaultSkillsSelected'))
                }
                renderTag={renderSummaryTag}
              >
                <Select.Option
                  value={AUTO_SELECT_VALUE}
                  className={styles.hiddenAutoOption}
                  wrapperClassName={styles.hiddenAutoOptionWrapper}
                >
                  {autoDefaultOptionLabel}
                </Select.Option>
                {editableSkillOptions.map((option) => (
                  <Select.Option key={option.value} value={option.value} disabled={option.disabled}>
                    {option.label}
                  </Select.Option>
                ))}
              </Select>
            ) : (
              <ReadonlySelectionField
                value={
                  defaultSkillsMode === 'auto'
                    ? autoDefaultOptionLabel
                    : readonlySelectionSummary(selectedSkillValues, t('settings.assistantNoDefaultSkillsSelected'))
                }
              />
            )}
          </ConfigRow>
        ) : null}

        <ConfigRow
          icon={<Toolkit theme='outline' size='14' />}
          label={t('settings.assistantDefaultMcpLabel')}
          hint={
            <Button
              type='text'
              size='mini'
              onClick={() => navigate('/settings/tools')}
              data-testid='btn-open-mcp-settings'
              className='!h-auto !px-0 !text-primary-6'
            >
              {t('settings.assistantOpenMcpSettings')}
            </Button>
          }
        >
          {canEditDefaultSkillsAndMcps ? (
            <Select
              className={styles.summarySelect}
              getPopupContainer={getEditorSelectPopupContainer}
              mode='multiple'
              value={defaultMcpMode === 'fixed' ? selectedMcpIds : [AUTO_SELECT_VALUE]}
              onChange={(value) => {
                const nextValues = ((value as string[]) ?? []).filter(Boolean);
                if (nextValues.length === 0) {
                  setDefaultMcpMode('fixed');
                  setSelectedMcpIds([]);
                  return;
                }

                const filteredValues = nextValues.filter((item) => item !== AUTO_SELECT_VALUE);
                if (filteredValues.length === 0) {
                  setDefaultMcpMode('auto');
                  setSelectedMcpIds([]);
                  return;
                }

                setDefaultMcpMode('fixed');
                setSelectedMcpIds(filteredValues);
              }}
              onClear={() => setDefaultMcpMode('auto')}
              allowClear
              maxTagCount={{
                count: 0,
                render: () =>
                  defaultMcpMode === 'auto' ? autoDefaultOptionLabel : selectedItemsLabel(selectedMcpIds.length),
              }}
              placeholder={defaultMcpMode === 'auto' ? autoDefaultOptionLabel : t('settings.assistantSelectDefaultMcp')}
              notFoundContent={t('settings.assistantNoAvailableMcps')}
              data-testid='select-assistant-default-mcp'
              dropdownRender={(menu) => (
                <div>
                  <Button
                    type='text'
                    size='small'
                    className='!mx-8px !mt-8px !justify-start !px-8px !text-primary-6'
                    onClick={(event) => {
                      event.preventDefault();
                      event.stopPropagation();
                      setDefaultMcpMode('auto');
                      setSelectedMcpIds([]);
                    }}
                  >
                    {autoDefaultOptionLabel}
                  </Button>
                  {menu}
                </div>
              )}
              renderFormat={() =>
                defaultMcpMode === 'auto'
                  ? autoDefaultOptionLabel
                  : readonlySelectionSummary(
                      enabledMcpServers
                        .filter((server) => selectedMcpIds.includes(server.id))
                        .map((server) => server.name),
                      t('settings.assistantNoDefaultMcpsSelected')
                    )
              }
              renderTag={renderSummaryTag}
            >
              <Select.Option
                value={AUTO_SELECT_VALUE}
                className={styles.hiddenAutoOption}
                wrapperClassName={styles.hiddenAutoOptionWrapper}
              >
                {autoDefaultOptionLabel}
              </Select.Option>
              {enabledMcpServers.map((server) => (
                <Select.Option key={server.id} value={server.id}>
                  {server.name}
                </Select.Option>
              ))}
            </Select>
          ) : (
            <ReadonlySelectionField
              value={
                defaultMcpMode === 'auto'
                  ? autoDefaultOptionLabel
                  : readonlySelectionSummary(
                      enabledMcpServers
                        .filter((server) => selectedMcpIds.includes(server.id))
                        .map((server) => server.name),
                      t('settings.assistantNoDefaultMcpsSelected')
                    )
              }
            />
          )}
        </ConfigRow>
      </div>
    </SectionCard>
  );
};

export default DefaultsSection;
