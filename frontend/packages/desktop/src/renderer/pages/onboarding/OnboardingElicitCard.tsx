import { Input } from '@arco-design/web-react';
import React from 'react';
import styles from './onboarding.module.css';

export type OnboardingTaskOption = { label: string; description: string };

type OnboardingElicitCardProps = {
  options: OnboardingTaskOption[];
  selected: string;
  customValue: string;
  customLabel: string;
  customPlaceholder: string;
  onSelect: (value: string) => void;
  onCustomChange: (value: string) => void;
};

const OnboardingElicitCard: React.FC<OnboardingElicitCardProps> = ({
  options,
  selected,
  customValue,
  customLabel,
  customPlaceholder,
  onSelect,
  onCustomChange,
}) => (
  <div className={styles.elicit} data-testid='onboarding-elicit-card'>
    {options.map((option, index) => (
      <button
        type='button'
        key={option.label}
        className={selected === option.label && !customValue.trim() ? styles.selectedTask : ''}
        aria-pressed={selected === option.label && !customValue.trim()}
        onClick={() => {
          onCustomChange('');
          onSelect(option.label);
        }}
      >
        <span className={styles.optionNumber}>{index + 1}</span>
        <span>
          <strong>{option.label}</strong>
          <small>{option.description}</small>
        </span>
      </button>
    ))}
    <label className={styles.fieldLabel} htmlFor='onboarding-task-custom'>
      {customLabel}
    </label>
    <Input
      id='onboarding-task-custom'
      value={customValue}
      placeholder={customPlaceholder}
      data-testid='onboarding-task-custom'
      onChange={(value) => {
        onCustomChange(value);
        if (value.trim()) onSelect('');
      }}
    />
  </div>
);

export default OnboardingElicitCard;
