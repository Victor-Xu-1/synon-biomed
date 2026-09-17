import { UploadOne } from '@icon-park/react';
import React, { useRef, useState } from 'react';
import styles from './onboarding.module.css';

type OnboardingDropZoneProps = {
  files: File[];
  onFiles: (files: File[]) => void;
  label: string;
  removeLabel: string;
};

const OnboardingDropZone: React.FC<OnboardingDropZoneProps> = ({ files, onFiles, label, removeLabel }) => {
  const inputRef = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);

  const append = (list: FileList | null) => {
    if (!list) return;
    const byIdentity = new Map(files.map((file) => [`${file.name}:${file.size}:${file.lastModified}`, file]));
    for (const file of Array.from(list)) byIdentity.set(`${file.name}:${file.size}:${file.lastModified}`, file);
    onFiles(Array.from(byIdentity.values()));
    if (inputRef.current) inputRef.current.value = '';
  };

  return (
    <div className={styles.dropZoneSlot} data-testid='onboarding-drop-zone'>
      <div
        className={`${styles.dropZone} ${dragging ? styles.dropZoneActive : ''}`}
        role='button'
        tabIndex={0}
        aria-label={label}
        onClick={() => inputRef.current?.click()}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            inputRef.current?.click();
          }
        }}
        onDragOver={(event) => {
          event.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(event) => {
          event.preventDefault();
          setDragging(false);
          append(event.dataTransfer.files);
        }}
      >
        <UploadOne size={22} aria-hidden />
        <span>{label}</span>
      </div>
      <input ref={inputRef} type='file' multiple hidden onChange={(event) => append(event.target.files)} />
      {files.length > 0 && (
        <ul className={styles.fileList} data-testid='onboarding-files'>
          {files.map((file) => (
            <li key={`${file.name}:${file.size}:${file.lastModified}`}>
              <span>{file.name}</span>
              <button
                type='button'
                aria-label={`${removeLabel}: ${file.name}`}
                onClick={() => onFiles(files.filter((candidate) => candidate !== file))}
              >
                {removeLabel}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
};

export default OnboardingDropZone;
